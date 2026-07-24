package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var errNotFound = errors.New("no staging build for this ticket")

type instance struct {
	ticket string
	cmd    *exec.Cmd
	socket string
	dir    string

	ready chan struct{} // closed once launch has succeeded or failed
	err   error         // set before ready is closed, on launch failure

	exited chan struct{} // closed once the process has actually exited

	mu         sync.Mutex
	lastAccess time.Time
}

type registry struct {
	mu  sync.Mutex
	m   map[string]*instance
	cfg *config
}

// Launches on demand; concurrent callers for the same cold ticket share
// one launch (single-flight) rather than each starting their own process.
func (r *registry) get_or_launch(ticket string) (*instance, error) {
	r.mu.Lock()
	if inst, ok := r.m[ticket]; ok {
		r.mu.Unlock()
		<-inst.ready
		if inst.err != nil {
			return nil, inst.err
		}
		inst.touch()
		return inst, nil
	}

	binary := filepath.Join(r.cfg.ticketsPath, ticket, "paver")
	if _, err := os.Stat(binary); err != nil {
		r.mu.Unlock()
		return nil, errNotFound
	}

	inst := &instance{ticket: ticket, ready: make(chan struct{})}
	r.m[ticket] = inst
	r.evict_over_cap_locked()
	r.mu.Unlock()

	logger.Printf("[INSTANCE start] ticket=%s", ticket)

	if err := r.launch(inst); err != nil {
		inst.err = err
		close(inst.ready)

		r.mu.Lock()
		if r.m[ticket] == inst {
			delete(r.m, ticket)
		}
		r.mu.Unlock()

		logger.Printf("[INSTANCE start-failed] ticket=%s err=%s", ticket, err.Error())
		return nil, err
	}

	inst.touch()
	close(inst.ready)

	logger.Printf("[INSTANCE ready] ticket=%s socket=%s", ticket, inst.socket)

	// deregister on unexpected crash so the next request relaunches cleanly
	go func() {
		<-inst.exited
		r.mu.Lock()
		if r.m[ticket] == inst {
			delete(r.m, ticket)
			logger.Printf("[INSTANCE crashed] ticket=%s", ticket)
		}
		r.mu.Unlock()
	}()

	return inst, nil
}

// No-op if the ticket is still cold-starting; the reaper cleans that up later.
func (r *registry) kill_if_running(ticket string, reason string) {
	r.mu.Lock()
	inst, ok := r.m[ticket]
	if !ok {
		r.mu.Unlock()
		return
	}

	select {
	case <-inst.ready:
	default:
		r.mu.Unlock()
		return
	}

	delete(r.m, ticket)
	r.mu.Unlock()

	if inst.err == nil {
		kill_instance(inst, reason)
	}
}

func (i *instance) touch() {
	i.mu.Lock()
	i.lastAccess = time.Now()
	i.mu.Unlock()
}

func (i *instance) idle_for() time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	return time.Since(i.lastAccess)
}

// Blocks until the socket is dialable, the process exits, or timeout.
func (r *registry) launch(inst *instance) error {
	dir := filepath.Join(r.cfg.runDir, inst.ticket)
	tmpdir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpdir, 0755); err != nil {
		return err
	}

	inst.dir = dir
	inst.socket = filepath.Join(dir, "paver.sock")
	logPath := filepath.Join(dir, "paver.log")
	binary := filepath.Join(r.cfg.ticketsPath, inst.ticket, "paver")

	os.Remove(inst.socket)

	cmd := exec.Command(binary,
		"-pubkey", r.cfg.pubkey,
		"-buckets", r.cfg.buckets,
		"-socket", inst.socket,
		"-tmpdir", tmpdir,
		"-log", logPath,
	)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", binary, err)
	}
	inst.cmd = cmd

	exited := make(chan struct{})
	inst.exited = exited
	go func() {
		cmd.Wait()
		close(exited)
	}()

	deadline := time.After(r.cfg.startupTimeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-exited:
			return fmt.Errorf("paver process for %s exited during startup", inst.ticket)
		case <-deadline:
			cmd.Process.Kill()
			<-exited
			return fmt.Errorf("paver process for %s did not become ready within %s", inst.ticket, r.cfg.startupTimeout)
		case <-tick.C:
			if conn, err := net.Dial("unix", inst.socket); err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

// Caller must hold r.mu. Only evicts already-ready instances.
func (r *registry) evict_over_cap_locked() {
	for len(r.m) > r.cfg.maxInstances {
		var lru *instance
		var lruTicket string

		for ticket, inst := range r.m {
			select {
			case <-inst.ready:
			default:
				continue // still launching, skip
			}
			if inst.err != nil {
				continue
			}
			if lru == nil || inst.idle_for() > lru.idle_for() {
				lru = inst
				lruTicket = ticket
			}
		}

		if lru == nil {
			return // nothing evictable right now (all still launching)
		}

		delete(r.m, lruTicket)
		go kill_instance(lru, "evicted (over max-instances)")
	}
}

func kill_instance(inst *instance, reason string) {
	logger.Printf("[INSTANCE reap] ticket=%s reason=%s", inst.ticket, reason)

	if inst.cmd != nil && inst.cmd.Process != nil {
		inst.cmd.Process.Signal(syscall.SIGTERM)

		select {
		case <-inst.exited:
		case <-time.After(5 * time.Second):
			inst.cmd.Process.Kill()
			<-inst.exited
		}
	}

	if inst.dir != "" {
		os.RemoveAll(inst.dir)
	}
}

func status_handler(r *registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		type instance_view struct {
			Ticket     string `json:"ticket"`
			State      string `json:"state"`
			LastAccess string `json:"last_access,omitempty"`
		}

		r.mu.Lock()
		views := make([]instance_view, 0, len(r.m))
		for ticket, inst := range r.m {
			v := instance_view{Ticket: ticket, State: "launching"}

			select {
			case <-inst.ready:
				if inst.err != nil {
					v.State = "failed"
				} else {
					v.State = "running"
					v.LastAccess = inst.lastAccess_str()
				}
			default:
			}

			views = append(views, v)
		}
		maxInstances := r.cfg.maxInstances
		r.mu.Unlock()

		b, _ := json.Marshal(map[string]any{
			"instances":     views,
			"max_instances": maxInstances,
		})

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
}

func (i *instance) lastAccess_str() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastAccess.Format(time.RFC3339)
}
