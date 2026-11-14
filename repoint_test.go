package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake_api is the part of PostgREST `paver repave` uses: the role RPC, cs and
// eq filters on processed_files, and PATCH returning the rows it changed.
type fake_api struct {
	mu           sync.Mutex
	token        string
	role         string
	datasets     map[string]*fake_dataset
	unavailable  int    // the next requests answer 503
	before_patch func() // runs between a dataset's read and its PATCH
	patches      int
}

type fake_dataset struct {
	geography string
	files     json.RawMessage
}

func (f *fake_api) serve(t *testing.T) *httptest.Server {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, `{"message":"JWT invalid"}`, http.StatusUnauthorized)
			return
		}

		if f.unavailable > 0 {
			f.unavailable--
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}

		q := r.URL.Query()

		switch {
		case r.Method == "POST" && r.URL.Path == "/rpc/current_user_role":
			json.NewEncoder(w).Encode(f.role)

		case r.Method == "GET" && r.URL.Path == "/datasets":
			rows := []map[string]any{}
			for id, d := range f.datasets {
				if v := q.Get("id"); v != "" && v != "eq."+id {
					continue
				}
				if v := q.Get("processed_files"); v != "" && !fake_contains(d.files, strings.TrimPrefix(v, "cs.")) {
					continue
				}
				if v := q.Get("geography_id"); v != "" && v != "eq."+d.geography {
					continue
				}
				rows = append(rows, map[string]any{"id": id, "geography_id": d.geography, "processed_files": d.files})
			}
			json.NewEncoder(w).Encode(rows)

		case r.Method == "PATCH" && r.URL.Path == "/datasets":
			if f.before_patch != nil {
				f.mu.Unlock()
				f.before_patch()
				f.before_patch = nil
				f.mu.Lock()

				if r.Header.Get("Authorization") != "Bearer "+f.token {
					http.Error(w, `{"message":"JWT expired"}`, http.StatusUnauthorized)
					return
				}
			}

			id := strings.TrimPrefix(q.Get("id"), "eq.")
			d := f.datasets[id]

			var body struct {
				Files json.RawMessage `json:"processed_files"`
			}
			json.NewDecoder(r.Body).Decode(&body)

			rows := []map[string]string{}
			if d != nil && json_equal(d.files, json.RawMessage(strings.TrimPrefix(q.Get("processed_files"), "eq."))) {
				d.files = body.Files
				f.patches++
				rows = append(rows, map[string]string{"id": id})
			}
			json.NewEncoder(w).Encode(rows)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)

	return s
}

func (f *fake_api) endpoints(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var entries []struct {
		Endpoint string `json:"endpoint"`
	}
	json.Unmarshal(f.datasets[id].files, &entries)

	var out []string
	for _, e := range entries {
		out = append(out, e.Endpoint)
	}

	return out
}

func fake_contains(files json.RawMessage, needle string) bool {
	var want []map[string]string
	if json.Unmarshal([]byte(needle), &want) != nil || len(want) != 1 {
		return false
	}

	return files_have(files, want[0]["endpoint"])
}

func files_have(files json.RawMessage, endpoint string) bool {
	var entries []struct {
		Endpoint string `json:"endpoint"`
	}
	json.Unmarshal(files, &entries)

	for _, e := range entries {
		if e.Endpoint == endpoint {
			return true
		}
	}

	return false
}

func json_equal(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}

	return reflect.DeepEqual(x, y)
}

func files_json(entries ...[2]string) json.RawMessage {
	var out []map[string]string
	for _, e := range entries {
		out = append(out, map[string]string{"func": e[0], "endpoint": e[1]})
	}

	j, _ := json.Marshal(out)

	return j
}

// repoint_fixture: d1 has vectors and the old raster, d2 is its clone, d3
// has a raster of its own. Old and new rasters are served: a rollback checks
// the object it restores exists.
func repoint_fixture(t *testing.T) (*fake_api, *postgrest, string, string, map[job_ref]repave_entry) {
	t.Helper()
	test_tmpdir(t)

	api_backoff = time.Millisecond
	t.Setenv(api_token_env, "secret")

	original := filepath.Join(tmpdir, "old.tif")
	os.WriteFile(original, []byte("old"), 0644)
	objects := serve_files(t, map[string]string{"/old": original, "/other": original, "/new": original})

	old, other, replacement := objects.URL+"/old", objects.URL+"/other", objects.URL+"/new"

	f := &fake_api{token: "secret", role: "director", datasets: map[string]*fake_dataset{
		"d1": {"g1", files_json([2]string{"vectors", "https://b/v1"}, [2]string{"raster", old})},
		"d2": {"g2", files_json([2]string{"raster", old})},
		"d3": {"g1", files_json([2]string{"raster", other})},
	}}

	p, err := postgrest_open(f.serve(t).URL)
	if err != nil {
		t.Fatal(err)
	}

	entries := map[job_ref]repave_entry{{old, ""}: {Old: old, New: replacement, Status: repave_done, Bucket: "world"}}

	return f, p, old, replacement, entries
}

func TestPostgrestRefusesTokensThatCannotSeeEveryDataset(t *testing.T) {
	f := &fake_api{token: "secret", role: "manager"}
	s := f.serve(t)

	t.Setenv(api_token_env, "")
	if _, err := postgrest_open(s.URL); err == nil || !strings.Contains(err.Error(), api_token_env) {
		t.Errorf("no token: %v", err)
	}

	t.Setenv(api_token_env, "secret")
	if _, err := postgrest_open(s.URL); err == nil || !strings.Contains(err.Error(), "manager") {
		t.Errorf("manager: %v", err)
	}

	t.Setenv(api_token_env, "expired")
	if _, err := postgrest_open(s.URL); !errors.Is(err, err_unauthorized) {
		t.Errorf("rejected token: %v", err)
	}

	f.role = "root"
	t.Setenv(api_token_env, "secret")
	if _, err := postgrest_open(s.URL); err != nil {
		t.Errorf("root: %v", err)
	}
}

func TestRepointMovesEveryDatasetThatReferencesTheEndpoint(t *testing.T) {
	f, p, old, replacement, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")
	todo := []repave_job{{Endpoint: old, DatasetID: "d1"}}

	// An entry written to another bucket is not this run's to apply.
	other := map[job_ref]repave_entry{{old, ""}: {Old: old, New: "https://c/new", Status: repave_done, Bucket: "india"}}
	repave_repoint(p, todo, other, "world", repoints)
	if f.patches != 0 {
		t.Errorf("applied another bucket's entry")
	}

	if err := repave_repoint(p, todo, entries, "world", repoints); err != nil {
		t.Fatal(err)
	}

	if got := f.endpoints("d1"); !reflect.DeepEqual(got, []string{"https://b/v1", replacement}) {
		t.Errorf("d1: %v", got)
	}
	if got := f.endpoints("d2"); !reflect.DeepEqual(got, []string{replacement}) {
		t.Errorf("clone: %v", got)
	}
	if got := f.endpoints("d3"); got[0] == replacement {
		t.Error("an unrelated dataset was repointed")
	}

	r, _ := repoints_open(repoints)
	if c := repoint_tally(r.lines); c[repoint_applied] != 2 || len(r.lines) != 2 {
		t.Errorf("%+v", r.lines)
	}
	if l := r.lines[0]; l.From != old || l.To != replacement || l.At == "" || l.Geography == "" {
		t.Errorf("%+v", l)
	}

	// A rerun has nothing to do.
	repave_repoint(p, todo, entries, "world", repoints)
	if f.patches != 2 {
		t.Errorf("%d patches", f.patches)
	}
}

func TestRepointKeepsARebuildInItsOwnGeography(t *testing.T) {
	f, p, old, replacement, _ := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")

	job := repave_job{Endpoint: old, Geography: "g1", Routine: "crop-raster"}
	entries := map[job_ref]repave_entry{job.ref(): {Old: old, New: replacement, Geography: "g1", Status: repave_done, Bucket: "world"}}

	repave_repoint(p, []repave_job{job}, entries, "world", repoints)

	if got := f.endpoints("d1"); got[1] != replacement {
		t.Errorf("d1: %v", got)
	}
	if got := f.endpoints("d2"); got[0] != old {
		t.Errorf("the clone in g2 got g1's rebuild: %v", got)
	}
}

func TestRepointNeverOverwritesAConcurrentEdit(t *testing.T) {
	f, p, old, replacement, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")
	todo := []repave_job{{Endpoint: old}}

	f.datasets = map[string]*fake_dataset{"d1": f.datasets["d1"]}
	f.before_patch = func() {
		f.mu.Lock()
		f.datasets["d1"].files = files_json([2]string{"vectors", "https://b/v2"}, [2]string{"raster", old})
		f.mu.Unlock()
	}

	repave_repoint(p, todo, entries, "world", repoints)

	if got := f.endpoints("d1"); got[0] != "https://b/v2" || got[1] != old {
		t.Errorf("the concurrent edit was overwritten: %v", got)
	}

	r, _ := repoints_open(repoints)
	if l := r.lines[len(r.lines)-1]; l.Status != repoint_failed || !strings.Contains(l.Error, err_changed.Error()) {
		t.Errorf("%+v", l)
	}

	// The next run reads the edit and keeps it.
	repave_repoint(p, todo, entries, "world", repoints)
	if got := f.endpoints("d1"); got[0] != "https://b/v2" || got[1] != replacement {
		t.Errorf("%v", got)
	}
}

func TestRepointRetriesAnUnavailableAPIAndStopsOnARejectedToken(t *testing.T) {
	f, p, old, _, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")
	todo := []repave_job{{Endpoint: old}}

	f.unavailable = 2
	if err := repave_repoint(p, todo, entries, "world", repoints); err != nil {
		t.Fatal(err)
	}
	if f.patches != 2 {
		t.Errorf("%d patches after two 503s", f.patches)
	}

	f2, p2, old2, _, entries2 := repoint_fixture(t)
	f2.token = "rotated"
	err := repave_repoint(p2, []repave_job{{Endpoint: old2}}, entries2, "world", filepath.Join(tmpdir, "m2.repoints.jsonl"))
	if !errors.Is(err, err_unauthorized) {
		t.Errorf("expired token: %v", err)
	}
	if f2.patches != 0 {
		t.Error("repointed with a rejected token")
	}

	// Expiring between a dataset's read and its write.
	f3, p3, old3, _, entries3 := repoint_fixture(t)
	f3.before_patch = func() {
		f3.mu.Lock()
		f3.token = "rotated"
		f3.mu.Unlock()
	}
	err = repave_repoint(p3, []repave_job{{Endpoint: old3}}, entries3, "world", filepath.Join(tmpdir, "m3.repoints.jsonl"))
	if !errors.Is(err, err_unauthorized) {
		t.Errorf("token expiring mid-run: %v", err)
	}
}

func TestRollbackRestoresTheOriginalEndpoints(t *testing.T) {
	f, p, old, replacement, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")
	todo := []repave_job{{Endpoint: old}}

	repave_repoint(p, todo, entries, "world", repoints)

	if err := repave_rollback(nil, repoints, "", ""); err != nil {
		t.Fatal(err)
	}
	if f.patches != 2 {
		t.Error("listing rolled back")
	}

	if err := repave_rollback(p, repoints, "", "d2"); err != nil {
		t.Fatal(err)
	}
	if got := f.endpoints("d2"); got[0] != old {
		t.Errorf("d2: %v", got)
	}
	if got := f.endpoints("d1"); got[1] != replacement {
		t.Errorf("the filter let d1 through: %v", got)
	}

	repave_rollback(p, repoints, "g1", "")
	if got := f.endpoints("d1"); !reflect.DeepEqual(got, []string{"https://b/v1", old}) {
		t.Errorf("d1: %v", got)
	}

	// Repeatable, and a later run does not undo the rollback.
	patches := f.patches
	repave_rollback(p, repoints, "", "")
	repave_repoint(p, todo, entries, "world", repoints)
	if f.patches != patches {
		t.Errorf("%d patches after a completed rollback", f.patches-patches)
	}
}

func TestRollbackUndoesSuccessiveRepointsNewestFirst(t *testing.T) {
	f, p, old, replacement, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")

	repave_repoint(p, []repave_job{{Endpoint: old}}, entries, "world", repoints)

	later := map[job_ref]repave_entry{{replacement, ""}: {Old: replacement, New: "https://b/newer", Status: repave_done, Bucket: "world"}}
	repave_repoint(p, []repave_job{{Endpoint: replacement}}, later, "world", repoints)

	if got := f.endpoints("d2"); got[0] != "https://b/newer" {
		t.Fatalf("%v", got)
	}

	repave_rollback(p, repoints, "", "")

	if got := f.endpoints("d2"); got[0] != old {
		t.Errorf("rolled back to %v, want the first endpoint", got)
	}
}

func TestRollbackLeavesWhatItCannotSafelyRestore(t *testing.T) {
	f, p, old, replacement, entries := repoint_fixture(t)
	repoints := filepath.Join(tmpdir, "m.repoints.jsonl")

	repave_repoint(p, []repave_job{{Endpoint: old}}, entries, "world", repoints)

	// d1 was re-paved from admin since; d2's original is gone.
	f.mu.Lock()
	f.datasets["d1"].files = files_json([2]string{"raster", "https://b/repaved"})
	f.mu.Unlock()

	r, _ := repoints_open(repoints)
	r.out.Close()
	for i := range r.lines {
		if r.lines[i].Dataset == "d2" {
			r.lines[i].From = strings.Replace(old, "/old", "/gone", 1)
		}
	}
	rewrite_repoints(t, repoints, r.lines)

	repave_rollback(p, repoints, "", "")

	if got := f.endpoints("d1"); got[0] != "https://b/repaved" {
		t.Errorf("rollback overwrote a later pave: %v", got)
	}
	if got := f.endpoints("d2"); got[0] != replacement {
		t.Errorf("rollback pointed at a missing object: %v", got)
	}

	r, _ = repoints_open(repoints)
	if c := repoint_tally(r.lines); c[repoint_failed] != 2 || c[repoint_restored] != 0 {
		t.Errorf("%+v", r.lines)
	}

	// A failed restore can be retried once its cause is fixed.
	f.mu.Lock()
	f.datasets["d1"].files = files_json([2]string{"raster", replacement})
	f.mu.Unlock()

	repave_rollback(p, repoints, "", "d1")
	if got := f.endpoints("d1"); got[0] != old {
		t.Errorf("retry: %v", got)
	}
}

func rewrite_repoints(t *testing.T, f filename, lines []repoint) {
	t.Helper()

	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(append(j, '\n'))
	}

	if err := os.WriteFile(f, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
}
