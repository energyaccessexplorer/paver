package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testSlotCapacity is routine_slots' capacity for the whole test binary.
// routine_slots and job_grace_period are set up once here and never
// reassigned again — reassigning a plain package variable while another
// goroutine (a still-finishing job) might read it is a real data race
// (caught by -race), regardless of how carefully a test waits first,
// because nothing establishes a happens-before edge between "the test
// observed job.done" and "that job's goroutine finished its post-done
// cleanup" (slot release happens via defer, strictly after done closes).
// So tests that need a smaller effective capacity or a busy queue borrow
// tokens from this shared channel instead of replacing it.
const testSlotCapacity = 8

func TestMain(m *testing.M) {
	logger = log.New(io.Discard, "", 0)
	routine_slots = make(chan struct{}, testSlotCapacity)
	job_grace_period = 30 * time.Millisecond
	os.Exit(m.Run())
}

// resetJobState gives each test a clean, isolated job registry and queue
// budget. queue_wait is safe to reassign per-test: every job started in a
// test is joined via <-job.done before the test returns, and queue_wait is
// only read once, at the very start of that job's goroutine (to size its
// timer) — strictly before that same goroutine's close(job.done), which
// is itself strictly before the next test's goroutine can start.
func resetJobState(wait time.Duration) {
	jobs_mu.Lock()
	jobs = map[string]*job{}
	jobs_mu.Unlock()

	queue_wait = wait
}

func newTestParams() routine_params {
	return routine_params{
		S3:      s3config{Name: "world", Key: "AKIA_TEST", Secret: "shh"},
		Dataset: "https://example.com/data.geojson",
	}
}

func noopReporter(string, ...any) string { return "" }

// testKey pads name to at least 12 bytes — job_get_or_start's logging does
// key[:12], which real callers satisfy via idempotency_key's sha256 hex
// (64 chars) but a short literal test key would not.
func testKey(name string) string {
	return name + "------------"
}

func jobsHas(key string) bool {
	jobs_mu.Lock()
	defer jobs_mu.Unlock()
	_, ok := jobs[key]
	return ok
}

// borrowSlots takes n tokens out of the shared routine_slots budget so a
// test can simulate a smaller effective capacity (or a fully busy queue),
// and returns a func that gives them back. Pure channel sends/receives on
// the one stable channel — safe under -race, unlike resizing the channel.
func borrowSlots(t *testing.T, n int) (release func()) {
	t.Helper()
	for i := 0; i < n; i++ {
		routine_slots <- struct{}{}
	}
	return func() {
		for i := 0; i < n; i++ {
			<-routine_slots
		}
	}
}

func TestJobGetOrStart_DedupAttach(t *testing.T) {
	resetJobState(time.Second)

	var calls int32
	release := make(chan struct{})
	fn := func(w reporter, p routine_params) (string, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return "result", nil
	}

	p := newTestParams()

	j1, started1 := job_get_or_start(testKey("keyA"), "r", noopReporter, p, fn)
	if !started1 {
		t.Fatal("expected the first call to start a new job")
	}

	j2, started2 := job_get_or_start(testKey("keyA"), "r", noopReporter, p, fn)
	if started2 {
		t.Fatal("expected the second call to attach to the existing job, not start another")
	}
	if j2 != j1 {
		t.Fatal("expected both callers to get the same job instance")
	}

	close(release)
	<-j1.done

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected fn to run exactly once, ran %d times", got)
	}
	if j1.result != "result" || j1.err != nil {
		t.Fatalf("unexpected job outcome: result=%q err=%v", j1.result, j1.err)
	}
}

// TestJobGetOrStart_ConcurrentDedup hammers job_get_or_start with the same
// key from many goroutines at once — the scenario the jobs_mu-guarded
// check-then-insert exists for. Run with -race.
func TestJobGetOrStart_ConcurrentDedup(t *testing.T) {
	resetJobState(time.Second)

	var calls int32
	release := make(chan struct{})
	fn := func(w reporter, p routine_params) (string, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return "result", nil
	}

	p := newTestParams()
	const n = 20

	results := make([]*job, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			j, _ := job_get_or_start(testKey("keyConcurrent"), "r", noopReporter, p, fn)
			results[i] = j
		}()
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Fatalf("expected all %d concurrent callers to attach to the same job instance", n)
		}
	}

	close(release)
	<-results[0].done

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected fn to run exactly once despite %d concurrent callers, ran %d times", n, got)
	}
}

// TestConcurrencyLimiter checks that routine_slots actually caps how many
// fn invocations run at once, rather than just tracking a count. It
// borrows testSlotCapacity-limit tokens so only `limit` are left available.
func TestConcurrencyLimiter(t *testing.T) {
	const limit = 2
	const jobCount = 6

	resetJobState(5 * time.Second)

	release := borrowSlots(t, testSlotCapacity-limit)
	defer release()

	var current, max int32
	fn := func(w reporter, p routine_params) (string, error) {
		n := atomic.AddInt32(&current, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m {
				break
			}
			if atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return "ok", nil
	}

	jobsList := make([]*job, jobCount)
	for i := 0; i < jobCount; i++ {
		p := newTestParams()
		p.Dataset = fmt.Sprintf("https://example.com/%d.geojson", i)

		j, started := job_get_or_start(testKey(fmt.Sprintf("key%d", i)), "r", noopReporter, p, fn)
		if !started {
			t.Fatalf("expected job %d to be a distinct, newly started job", i)
		}
		jobsList[i] = j
	}

	for _, j := range jobsList {
		<-j.done
	}

	if got := atomic.LoadInt32(&max); got > limit {
		t.Fatalf("expected at most %d concurrent fn invocations, observed %d", limit, got)
	}
}

// TestFinishBusy_NotCached checks that a queue-timeout (503) is neither
// mistaken for a real result nor cached under the idempotency key — a
// retry once a slot frees up must get a fresh attempt.
func TestFinishBusy_NotCached(t *testing.T) {
	resetJobState(30 * time.Millisecond)

	// Borrow every slot so the next job can't acquire one before
	// queue_wait elapses.
	release := borrowSlots(t, testSlotCapacity)

	var calls int32
	fn := func(w reporter, p routine_params) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}

	p := newTestParams()

	j, started := job_get_or_start(testKey("keyBusy"), "r", noopReporter, p, fn)
	if !started {
		t.Fatal("expected a new job to be registered")
	}
	<-j.done

	if !errors.Is(j.err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", j.err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("fn must not run when the job never acquires a slot, ran %d times", got)
	}
	if jobsHas(testKey("keyBusy")) {
		t.Fatal("a queue-timeout must not leave the job cached under its key")
	}

	// Free the slots and retry: this must be a fresh attempt, not a replay
	// of the cached ErrBusy.
	release()

	j2, started2 := job_get_or_start(testKey("keyBusy"), "r", noopReporter, p, fn)
	if !started2 {
		t.Fatal("expected the retry to start a fresh job, not attach to a cached busy result")
	}
	<-j2.done

	if j2.err != nil {
		t.Fatalf("expected the retry to succeed, got %v", j2.err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected fn to run exactly once on retry, ran %d times", got)
	}
}

// TestGracePeriod_CachesThenExpires checks that a finished job's result is
// replayed instantly to a retry within job_grace_period, and that a retry
// after the grace period runs the routine again. job_grace_period is set
// once in TestMain (30ms) so this test doesn't need to sleep 60s.
func TestGracePeriod_CachesThenExpires(t *testing.T) {
	resetJobState(time.Second)

	var calls int32
	fn := func(w reporter, p routine_params) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}

	p := newTestParams()

	j1, started1 := job_get_or_start(testKey("keyGrace"), "r", noopReporter, p, fn)
	if !started1 {
		t.Fatal("expected a new job")
	}
	<-j1.done

	// Immediately after finishing: still within the grace period, so a
	// retry attaches to the cached job instead of re-running fn.
	j2, started2 := job_get_or_start(testKey("keyGrace"), "r", noopReporter, p, fn)
	if started2 {
		t.Fatal("expected the retry to attach to the cached finished job")
	}
	if j2 != j1 {
		t.Fatal("expected the same job instance while within the grace period")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fn must not re-run while the cached result is still fresh, ran %d times", got)
	}

	// Past the grace period: the cache entry is gone, so the retry is a
	// fresh run.
	time.Sleep(3 * job_grace_period)

	j3, started3 := job_get_or_start(testKey("keyGrace"), "r", noopReporter, p, fn)
	if !started3 {
		t.Fatal("expected a fresh job once the grace period has expired")
	}
	<-j3.done

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected fn to re-run once the grace period expired, ran %d times", got)
	}
}

func TestRunDeduped(t *testing.T) {
	resetJobState(time.Second)

	fn := func(w reporter, p routine_params) (string, error) {
		return "hello", nil
	}

	result, err := run_deduped(testKey("keyRun"), "r", noopReporter, newTestParams(), fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Fatalf("expected %q, got %q", "hello", result)
	}
}

func TestIdempotencyKey(t *testing.T) {
	base := newTestParams()
	base.Resolution = 100

	k1 := idempotency_key("simplify", base)

	// S3 credentials are excluded from job identity by design (comment on
	// job_identity) — only the bucket's logical Name matters.
	withDifferentCreds := base
	withDifferentCreds.S3.Key = "another-key"
	withDifferentCreds.S3.Secret = "another-secret"
	if idempotency_key("simplify", withDifferentCreds) != k1 {
		t.Error("S3 credentials must not affect the idempotency key")
	}

	withDifferentRoutine := base
	if idempotency_key("crop_raster", withDifferentRoutine) == k1 {
		t.Error("different routine names must produce different keys")
	}

	withDifferentDataset := base
	withDifferentDataset.Dataset = "https://example.com/other.geojson"
	if idempotency_key("simplify", withDifferentDataset) == k1 {
		t.Error("different dataset URLs must produce different keys")
	}

	withDifferentBucketName := base
	withDifferentBucketName.S3.Name = "other-bucket"
	if idempotency_key("simplify", withDifferentBucketName) == k1 {
		t.Error("different S3 bucket names must produce different keys")
	}

	// Sanity check: identical input is deterministic.
	if idempotency_key("simplify", base) != k1 {
		t.Error("identical params must produce the same key")
	}
}
