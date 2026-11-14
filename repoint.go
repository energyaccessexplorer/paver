package main

// Datasets are repointed through PostgREST, under the same row security and
// triggers as admin's edits, with a token for a user who can see every
// dataset. Each change is appended to a repoints file -- a dataset, the
// endpoint it had and the one it got -- which is all a rollback needs: the old
// objects are never deleted.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const api_token_env = "PAVER_API_TOKEN"

var err_unauthorized = errors.New("the API refused the token")

var err_changed = errors.New("the dataset changed while it was repointed")

// Backoff before the second and third attempt is api_backoff and 4x that.
var api_backoff = time.Second

type postgrest struct {
	url   string
	token string
}

// postgrest_open checks the token before anything is written: row security
// hides private datasets from every other role, so a weaker token would find
// nothing to repoint instead of failing.
func postgrest_open(base string) (*postgrest, error) {
	token := os.Getenv(api_token_env)
	if token == "" {
		return nil, fmt.Errorf("-api needs a token in %s", api_token_env)
	}

	p := &postgrest{url: base, token: token}

	var role string
	if err := p.do("POST", "rpc/current_user_role", nil, struct{}{}, &role); err != nil {
		return nil, err
	}

	if role != "director" && role != "root" {
		return nil, fmt.Errorf("the token's role is %q: repointing every dataset needs director or root", role)
	}

	return p, nil
}

// do retries network errors and 5xx; any 4xx is final.
func (p *postgrest) do(method, path string, query url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	u := p.url + "/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var err error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(api_backoff << (2 * (attempt - 1)))
		}

		var retry bool
		if retry, err = p.once(method, u, payload, out); !retry {
			return err
		}
	}

	return err
}

func (p *postgrest) once(method, u string, payload []byte, out any) (retry bool, err error) {
	q, err := http.NewRequest(method, u, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}

	q.Header.Set("Authorization", "Bearer "+p.token)
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Accept", "application/json")
	if method == "PATCH" {
		q.Header.Set("Prefer", "return=representation")
	}

	r, err := http_client.Do(q)
	if err != nil {
		return true, err
	}
	defer r.Body.Close()

	b, err := io.ReadAll(r.Body)
	if err != nil {
		return true, err
	}

	switch {
	case r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("%w: %s %s", err_unauthorized, r.Status, b)
	case r.StatusCode >= 500:
		return true, fmt.Errorf("%s %s: %s %s", method, u, r.Status, b)
	case r.StatusCode >= 400:
		return false, fmt.Errorf("%s %s: %s %s", method, u, r.Status, b)
	}

	if out == nil {
		return false, nil
	}

	return false, json.Unmarshal(b, out)
}

type api_dataset struct {
	ID        string          `json:"id"`
	Geography string          `json:"geography_id"`
	Files     json.RawMessage `json:"processed_files"`
}

const api_dataset_select = "id,geography_id,processed_files"

// referencing returns the datasets of geography (any, if "") with an entry
// for endpoint: clones share their original's files.
func (p *postgrest) referencing(endpoint, geography string) ([]api_dataset, error) {
	contains, _ := json.Marshal([]map[string]string{{"endpoint": endpoint}})

	query := url.Values{"select": {api_dataset_select}, "processed_files": {"cs." + string(contains)}}
	if geography != "" {
		query.Set("geography_id", "eq."+geography)
	}

	var ds []api_dataset
	err := p.do("GET", "datasets", query, nil, &ds)

	return ds, err
}

func (p *postgrest) dataset(id string) (*api_dataset, error) {
	var ds []api_dataset
	if err := p.do("GET", "datasets", url.Values{"select": {api_dataset_select}, "id": {"eq." + id}}, nil, &ds); err != nil {
		return nil, err
	}

	if len(ds) == 0 {
		return nil, fmt.Errorf("dataset %s not found", id)
	}

	return &ds[0], nil
}

// swap points d's entries for from at to, only if processed_files is still
// what d was read with.
func (p *postgrest) swap(d api_dataset, from, to string) error {
	files, n, err := files_repointed(d.Files, from, to)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("dataset %s has no entry for %s: re-paved from admin since?", d.ID, from)
	}

	var rows []struct {
		ID string `json:"id"`
	}

	query := url.Values{"select": {"id"}, "id": {"eq." + d.ID}, "processed_files": {"eq." + string(d.Files)}}
	if err := p.do("PATCH", "datasets", query, map[string]json.RawMessage{"processed_files": files}, &rows); err != nil {
		return err
	}

	if len(rows) != 1 {
		return err_changed
	}

	return nil
}

// files_repointed rewrites the endpoint of every entry that has from, and
// keeps everything else as it was.
func files_repointed(files json.RawMessage, from, to string) (json.RawMessage, int, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(files, &entries); err != nil {
		return nil, 0, err
	}

	target, _ := json.Marshal(to)

	n := 0
	for _, e := range entries {
		var endpoint string
		if json.Unmarshal(e["endpoint"], &endpoint) == nil && endpoint == from {
			e["endpoint"] = target
			n++
		}
	}

	out, err := json.Marshal(entries)

	return out, n, err
}

type repoint struct {
	At        string `json:"at"`
	Dataset   string `json:"dataset"`
	Geography string `json:"geography,omitempty"`
	From      string `json:"from"`
	To        string `json:"to"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

const (
	repoint_applied  = "applied"
	repoint_restored = "restored"
	repoint_failed   = "failed" // a record only: the pair stays as it was
)

type repoint_pair struct{ dataset, from string }

type repoints struct {
	lines []repoint
	out   *os.File
}

func repoints_open(f filename) (*repoints, error) {
	r := &repoints{}

	if file, err := os.Open(f); err == nil {
		sc := bufio.NewScanner(file)
		sc.Buffer(make([]byte, 1<<20), 1<<20)

		for sc.Scan() {
			var l repoint
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				file.Close()
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			r.lines = append(r.lines, l)
		}

		file.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	out, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	r.out = out

	return r, nil
}

func (r *repoints) add(l repoint) error {
	l.At = time.Now().UTC().Format(time.RFC3339)
	r.lines = append(r.lines, l)

	logger.Printf("%-8s %s %s -> %s %s", l.Status, l.Dataset, l.From, l.To, l.Error)

	j, _ := json.Marshal(l)
	_, err := r.out.Write(append(j, '\n'))

	return err
}

// state is each pair's last applied or restored line, by index.
func (r *repoints) state() map[repoint_pair]int {
	s := map[repoint_pair]int{}
	for i, l := range r.lines {
		if l.Status == repoint_applied || l.Status == repoint_restored {
			s[repoint_pair{l.Dataset, l.From}] = i
		}
	}

	return s
}

func repoint_tally(lines []repoint) map[string]int {
	c := map[string]int{}
	for _, l := range lines {
		c[l.Status]++
	}

	return c
}

// repave_repoint points the datasets of a job's geography that reference its
// endpoint at what the job wrote to bucket. A pair already applied, or
// restored by a rollback, is left alone.
func repave_repoint(p *postgrest, todo []repave_job, entries map[job_ref]repave_entry, bucket string, f filename) error {
	r, err := repoints_open(f)
	if err != nil {
		return err
	}
	defer r.out.Close()

	settled := r.state()
	from := len(r.lines)

	defer func() {
		c := repoint_tally(r.lines[from:])
		fmt.Printf("repointed: applied %d, failed %d (%s)\n", c[repoint_applied], c[repoint_failed], f)
	}()

	for _, job := range todo {
		e, ok := entries[job.ref()]
		if !ok || e.Status != repave_done || e.Bucket != bucket {
			continue
		}

		ds, err := p.referencing(e.Old, job.Geography)
		if errors.Is(err, err_unauthorized) {
			return err
		}
		if err != nil {
			if err := r.add(repoint{Dataset: job.DatasetID, Geography: job.Geography, From: e.Old, To: e.New, Status: repoint_failed, Error: err.Error()}); err != nil {
				return err
			}
			continue
		}

		for _, d := range ds {
			if _, done := settled[repoint_pair{d.ID, e.Old}]; done {
				continue
			}

			l := repoint{Dataset: d.ID, Geography: d.Geography, From: e.Old, To: e.New, Status: repoint_applied}

			err := p.swap(d, e.Old, e.New)
			if errors.Is(err, err_unauthorized) {
				return err
			}
			if err != nil {
				l.Status, l.Error = repoint_failed, err.Error()
			}

			if err := r.add(l); err != nil {
				return err
			}
		}
	}

	return nil
}

// repave_rollback points datasets back at the endpoints they had, newest
// change first, so a dataset repointed twice ends where it started. Without p
// it only lists what it would restore.
func repave_rollback(p *postgrest, f filename, geography, dataset string) error {
	r, err := repoints_open(f)
	if err != nil {
		return err
	}
	defer r.out.Close()

	state := r.state()

	var todo []repoint
	for i := len(r.lines) - 1; i >= 0; i-- {
		l := r.lines[i]
		if l.Status != repoint_applied || state[repoint_pair{l.Dataset, l.From}] != i {
			continue
		}
		if (geography != "" && l.Geography != geography) || (dataset != "" && l.Dataset != dataset) {
			continue
		}
		todo = append(todo, l)
	}

	if p == nil {
		for _, l := range todo {
			fmt.Printf("would restore %s %s: %s -> %s\n", l.Geography, l.Dataset, l.To, l.From)
		}
		fmt.Printf("%d to restore; pass -api to restore them\n", len(todo))
		return nil
	}

	from := len(r.lines)

	for _, l := range todo {
		back := repoint{Dataset: l.Dataset, Geography: l.Geography, From: l.From, To: l.To, Status: repoint_restored}

		if err := rollback_one(p, l); err != nil {
			if errors.Is(err, err_unauthorized) {
				return err
			}
			back.Status, back.Error = repoint_failed, err.Error()
		}

		if err := r.add(back); err != nil {
			return err
		}
	}

	c := repoint_tally(r.lines[from:])
	fmt.Printf("rolled back: restored %d, failed %d (%s)\n", c[repoint_restored], c[repoint_failed], f)

	return nil
}

func rollback_one(p *postgrest, l repoint) error {
	if _, err := object_version(l.From); err != nil {
		return fmt.Errorf("the original is gone: %w", err)
	}

	d, err := p.dataset(l.Dataset)
	if err != nil {
		return err
	}

	return p.swap(*d, l.To, l.From)
}
