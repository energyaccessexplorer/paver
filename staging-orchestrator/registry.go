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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var errNotFound = errors.New("no staging build for this ticket")

const (
	servicePaver    = "paver"
	serviceDeparter = "departer"
)

// Instances are keyed by "<TICKET>/<service>": a ticket can have a paver and a
// departer instance at the same time, each with its own socket and lifetime.
func instance_key(ticket, service string) string { return ticket + "/" + service }

type instance struct {
	ticket  string
	service string
	cmd     *exec.Cmd
	socket  string
	dir     string

	ready chan struct{} // closed once launch has succeeded or failed
	err   error         // set before ready is closed, on launch failure

	exited chan struct{} // closed once the process has actually exited

	active atomic.Int64 // in-flight proxied requests; a busy instance is never reaped

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
func (r *registry) get_or_launch(ticket, service string) (*instance, error) {
	key := instance_key(ticket, service)

	r.mu.Lock()
	if inst, ok := r.m[key]; ok {
		r.mu.Unlock()
		<-inst.ready
		if inst.err != nil {
			return nil, inst.err
		}
		inst.touch()
		return inst, nil
	}

	binary := filepath.Join(r.cfg.ticketsPath, ticket, service)
	if _, err := os.Stat(binary); err != nil {
		r.mu.Unlock()
		return nil, errNotFound
	}

	inst := &instance{ticket: ticket, service: service, ready: make(chan struct{})}
	r.m[key] = inst
	r.evict_over_cap_locked()
	r.mu.Unlock()

	logger.Printf("[INSTANCE start] ticket=%s service=%s", ticket, service)

	if err := r.launch(inst); err != nil {
		inst.err = err
		close(inst.ready)

		r.mu.Lock()
		if r.m[key] == inst {
			delete(r.m, key)
		}
		r.mu.Unlock()

		logger.Printf("[INSTANCE start-failed] ticket=%s service=%s err=%s", ticket, service, err.Error())
		return nil, err
	}

	inst.touch()
	close(inst.ready)

	logger.Printf("[INSTANCE ready] ticket=%s service=%s socket=%s", ticket, service, inst.socket)

	// deregister on unexpected crash so the next request relaunches cleanly
	go func() {
		<-inst.exited
		r.mu.Lock()
		if r.m[key] == inst {
			delete(r.m, key)
			logger.Printf("[INSTANCE crashed] ticket=%s service=%s", ticket, service)
		}
		r.mu.Unlock()
	}()

	return inst, nil
}

// No-op if the ticket is still cold-starting; the reaper cleans that up later.
func (r *registry) kill_if_running(ticket, service string, reason string) {
	key := instance_key(ticket, service)

	r.mu.Lock()
	inst, ok := r.m[key]
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

	delete(r.m, key)
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

func (i *instance) busy() bool {
	return i.active.Load() > 0
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
	inst.socket = filepath.Join(dir, inst.service+".sock")
	binary := filepath.Join(r.cfg.ticketsPath, inst.ticket, inst.service)

	os.Remove(inst.socket)

	var cmd *exec.Cmd

	switch inst.service {
	case serviceDeparter:
		// departer writes its log and the finished zip into the tmpdir it is
		// given, and prints "<prefix>/energyaccessexplorer-<id>.zip" as the
		// download link — so the tmpdir is persistent (survives reaping) and
		// the prefix is this ticket's public path.
		builds := filepath.Join(r.cfg.departerBuilds, inst.ticket)
		if err := os.MkdirAll(builds, 0755); err != nil {
			return err
		}

		script := filepath.Join(dir, "departer.sh")
		if err := write_departer_script(script, r.cfg.departerWorkspace, "/"+inst.ticket+"/departer/builds", builds, r.cfg.publicOrigin+"/"+inst.ticket); err != nil {
			return err
		}

		cmd = exec.Command(binary,
			"-script", script,
			"-pubkey", r.cfg.pubkey,
			"-socket", inst.socket,
			"-tmpdir", builds,
		)

	default:
		logPath := filepath.Join(dir, "paver.log")
		cmd = exec.Command(binary,
			"-pubkey", r.cfg.pubkey,
			"-buckets", r.cfg.buckets,
			"-socket", inst.socket,
			"-tmpdir", tmpdir,
			"-log", logPath,
		)
	}

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
			return fmt.Errorf("%s process for %s exited during startup", inst.service, inst.ticket)
		case <-deadline:
			cmd.Process.Kill()
			<-exited
			return fmt.Errorf("%s process for %s did not become ready within %s", inst.service, inst.ticket, r.cfg.startupTimeout)
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
			if inst.busy() {
				continue
			}
			if lru == nil || inst.idle_for() > lru.idle_for() {
				lru = inst
				lruTicket = ticket
			}
		}

		if lru == nil {
			return // nothing evictable right now (all still launching or busy)
		}

		delete(r.m, lruTicket)
		go kill_instance(lru, "evicted (over max-instances)")
	}
}

// The script departer runs per build. Paths that differ per ticket are baked in
// here; the build id and target OS still arrive as arguments ($1 tmpdir, $2 id,
// $3 os), so nothing else needs to be templated. public_base (e.g.
// https://paver.energyaccessexplorer.org/EAE-506) is prepended to the download
// link the log ends with, so it is absolute — and clickable — in the CMS.
func write_departer_script(path, workspace, static_prefix, builds, public_base string) error {
	body := "#!/bin/sh\n" +
		"cd " + shell_quote(workspace) + " || exit 1\n" +
		"if ! IDSFILE=$1/$2 ID=$2 bmake gobuild website tool fetch zip os=${3}; then\n" +
		"\texit 1\n" +
		"fi\n" +
		"mv energyaccessexplorer-$2.zip " + shell_quote(builds) + "/\n" +
		"echo " + shell_quote(public_base+static_prefix) + "/energyaccessexplorer-$2.zip\n"

	return os.WriteFile(path, []byte(body), 0755)
}

func shell_quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func kill_instance(inst *instance, reason string) {
	logger.Printf("[INSTANCE reap] ticket=%s service=%s reason=%s", inst.ticket, inst.service, reason)

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
			Service    string `json:"service"`
			State      string `json:"state"`
			LastAccess string `json:"last_access,omitempty"`
		}

		r.mu.Lock()
		views := make([]instance_view, 0, len(r.m))
		for key, inst := range r.m {
			v := instance_view{Ticket: inst.ticket, Service: inst.service, State: "launching"}
			_ = key

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
