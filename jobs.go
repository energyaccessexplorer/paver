package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const job_grace_period = 60 * time.Second

var ErrBusy = errors.New("server busy: too many concurrent paver jobs, try again shortly")

type job_state string

const (
	job_running job_state = "running"
	job_done    job_state = "done"
	job_error   job_state = "error"
)

type job struct {
	key     string
	routine string
	state   job_state
	result  string
	err     error
	started time.Time
	done    chan struct{}
}

var (
	jobs_mu sync.Mutex
	jobs    = map[string]*job{}
)

var (
	routine_slots  chan struct{}
	max_concurrent int
	queue_wait     time.Duration
)

// job_identity is the subset of routine_params that determines a job's
// logical identity. Deliberately excludes S3.Key/S3.Secret (credentials,
// not job identity, and must never end up in a hash or a log).
type job_identity struct {
	Routine    string        `json:"routine"`
	S3Name     string        `json:"s3name"`
	Dataset    string        `json:"dataseturl"`
	Reference  string        `json:"referenceurl"`
	Base       string        `json:"baseurl"`
	Resolution int           `json:"resolution"`
	Simplify   float32       `json:"simplify"`
	LngLat     [2]string     `json:"lnglat"`
	Attr       string        `json:"attr"`
	Fields     []string      `json:"fields"`
	Config     raster_config `json:"config"`
	Dissolve   bool          `json:"dissolve"`
}

func idempotency_key(routine string, p routine_params) string {
	b, _ := json.Marshal(job_identity{
		Routine:    routine,
		S3Name:     p.S3.Name,
		Dataset:    p.Dataset,
		Reference:  p.Reference,
		Base:       p.Base,
		Resolution: p.Resolution,
		Simplify:   p.Simplify,
		LngLat:     p.LngLat,
		Attr:       p.Attr,
		Fields:     p.Fields,
		Config:     p.Config,
		Dissolve:   p.Dissolve,
	})

	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// job_get_or_start returns the job registered under key — attaching to an
// already-running (or recently-finished, within job_grace_period) identical
// job — or registers a new one and runs it in a goroutine behind the
// concurrency-limiting semaphore. started reports which of the two happened.
// Callers either block on <-j.done or inspect the job non-blockingly.
func job_get_or_start(key string, routine_name string, w reporter, p routine_params, fn routine) (j *job, started bool) {
	jobs_mu.Lock()
	if existing, ok := jobs[key]; ok {
		jobs_mu.Unlock()
		logger.Printf("[JOB dedup] key=%s routine=%s attaching to existing job", key[:12], routine_name)
		return existing, false
	}

	j = &job{
		key:     key,
		routine: routine_name,
		state:   job_running,
		started: time.Now(),
		done:    make(chan struct{}),
	}
	jobs[key] = j
	jobs_mu.Unlock()

	logger.Printf("[JOB start] key=%s routine=%s dataseturl=%s", key[:12], routine_name, p.Dataset)

	go func() {
		select {
		case routine_slots <- struct{}{}:
			defer func() { <-routine_slots }()
		case <-time.After(queue_wait):
			logger.Printf("[JOB queue-timeout] key=%s routine=%s slots=%d/%d", key[:12], routine_name, len(routine_slots), cap(routine_slots))
			finish_busy(j, key)
			return
		}

		result, err := fn(w, p)

		duration := time.Since(j.started)
		if err != nil {
			logger.Printf("[JOB error] key=%s routine=%s duration=%s err=%s", key[:12], routine_name, duration, err.Error())
		} else {
			logger.Printf("[JOB done] key=%s routine=%s duration=%s", key[:12], routine_name, duration)
		}

		finish(j, key, result, err)
	}()

	return j, true
}

// run_deduped runs (or attaches to) the job under key and blocks until it
// finishes. Callers always get back the same (jsonstr, err) shape a direct
// rtn.fn() call would have produced.
func run_deduped(key string, routine_name string, w reporter, p routine_params, fn routine) (string, error) {
	j, _ := job_get_or_start(key, routine_name, w, p, fn)
	<-j.done
	return j.result, j.err
}

// finish records a real outcome (success or a genuine routine error) and
// keeps it cached under key for job_grace_period, so a retry that lands
// while it's still fresh gets the same answer instantly instead of
// re-running.
func finish(j *job, key string, result string, err error) (string, error) {
	j.result, j.err = result, err
	if err != nil {
		j.state = job_error
	} else {
		j.state = job_done
	}
	close(j.done)

	time.AfterFunc(job_grace_period, func() {
		jobs_mu.Lock()
		if jobs[key] == j {
			delete(jobs, key)
		}
		jobs_mu.Unlock()
	})

	return result, err
}

// finish_busy records a queue-timeout (ErrBusy) outcome, but does NOT cache
// it: a "server busy" response is about capacity at this instant, not
// job identity, so the next retry deserves a fresh shot at a slot rather
// than instantly replaying the same 503 for job_grace_period.
func finish_busy(j *job, key string) (string, error) {
	j.result, j.err = "", ErrBusy
	j.state = job_error
	close(j.done)

	jobs_mu.Lock()
	if jobs[key] == j {
		delete(jobs, key)
	}
	jobs_mu.Unlock()

	return "", ErrBusy
}

func _status(w http.ResponseWriter, r *http.Request) {
	jobs_mu.Lock()
	defer jobs_mu.Unlock()

	type job_view struct {
		Key       string `json:"key"`
		Routine   string `json:"routine"`
		State     string `json:"state"`
		StartedAt string `json:"started_at"`
	}

	views := make([]job_view, 0, len(jobs))
	for _, j := range jobs {
		views = append(views, job_view{
			Key:       j.key[:12],
			Routine:   j.routine,
			State:     string(j.state),
			StartedAt: j.started.Format(time.RFC3339),
		})
	}

	b, _ := json.Marshal(map[string]any{
		"in_flight":      len(jobs),
		"slots_in_use":   len(routine_slots),
		"queue_capacity": cap(routine_slots),
		"jobs":           views,
	})

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, string(b))
}
