package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

// fake_storage stands in for Supabase Storage: objects are PUT to the S3 API
// path-style and read back from the public URL prefix.
type fake_storage struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string
}

const fake_public = "/storage/v1/object/public"

func (f *fake_storage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case "PUT":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		f.objects[r.URL.Path] = body
		f.puts = append(f.puts, r.URL.Path)
		w.WriteHeader(http.StatusOK)

	case "GET", "HEAD":
		if key, ok := strings.CutPrefix(r.URL.Path, fake_public); ok {
			body, found := f.objects[key]
			if !found {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			if r.Method == "GET" {
				w.Write(body)
			}

			return
		}

		http.NotFound(w, r)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// supabase_bucket is the local stack's storage entry: the API is the test
// server, and so is the public URL base.
func supabase_bucket(srv *httptest.Server) s3config {
	return s3config{
		Name:      "local",
		Provider:  supabase_provider,
		Endpoint:  srv.URL,
		Public:    srv.URL + fake_public,
		Bucket:    "geotiff-test",
		Directory: "repave",
		Region:    "local",
		Key:       "access-key-id",
		Secret:    "secret-access-key",
		Scratch:   true,
	}
}

func TestS3EndpointShapes(t *testing.T) {
	local := s3config{Provider: local_provider, Bucket: "http://host/geotiff-test", Directory: "/srv/dir"}
	if got, want := s3endpoint(local, "k"), "http://host/geotiff-test/k"; got != want {
		t.Errorf("local: %s, want %s", got, want)
	}

	aws := s3config{Provider: "s3.amazonaws.com", Bucket: "wri-public-data", Directory: "EnergyAccess/paver-outputs"}
	if got, want := s3endpoint(aws, "k"), "https://wri-public-data.s3.amazonaws.com/EnergyAccess/paver-outputs/k"; got != want {
		t.Errorf("aws: %s, want %s", got, want)
	}

	supabase := s3config{
		Provider:  supabase_provider,
		Public:    "http://eae.localhost/storage/v1/object/public/",
		Bucket:    "geotiff-test",
		Directory: "repave",
	}
	if got, want := s3endpoint(supabase, "k"), "http://eae.localhost/storage/v1/object/public/geotiff-test/repave/k"; got != want {
		t.Errorf("supabase: %s, want %s", got, want)
	}

	supabase.Directory = ""
	if got, want := s3endpoint(supabase, "k"), "http://eae.localhost/storage/v1/object/public/geotiff-test/k"; got != want {
		t.Errorf("supabase without a prefix: %s, want %s", got, want)
	}
}

func TestS3PresignedSupabaseUsesPathStyle(t *testing.T) {
	conf := supabase_bucket(httptest.NewServer(http.NotFoundHandler()))

	url, err := s3presigned(conf, "PUT", "abc")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(url, conf.Endpoint+"/geotiff-test/repave/abc?") {
		t.Fatalf("not a path-style PUT to the configured endpoint: %s", url)
	}

	r, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		t.Fatal(err)
	}

	q := r.URL.Query()
	for _, p := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-Signature"} {
		if q.Get(p) == "" {
			t.Errorf("missing %s in %s", p, url)
		}
	}

	if got, want := q.Get("X-Amz-Algorithm"), "AWS4-HMAC-SHA256"; got != want {
		t.Errorf("algorithm %s, want %s", got, want)
	}

	if !strings.HasPrefix(q.Get("X-Amz-Credential"), conf.Key+"/") || !strings.Contains(q.Get("X-Amz-Credential"), "/"+conf.Region+"/s3/aws4_request") {
		t.Errorf("credential %s does not scope the bucket's key and region", q.Get("X-Amz-Credential"))
	}

	other, err := s3presigned(conf, "GET", "abc")
	if err != nil {
		t.Fatal(err)
	}

	if other == url {
		t.Error("a GET presigned the same URL as a PUT")
	}

	if !strings.HasPrefix(other, conf.Endpoint+"/geotiff-test/repave/abc?") {
		t.Errorf("GET is not path-style either: %s", other)
	}
}

func TestS3PutAndHasSupabase(t *testing.T) {
	st := &fake_storage{objects: map[string][]byte{}}
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)

	conf := supabase_bucket(srv)

	test_tmpdir(t)
	file := filepath.Join(tmpdir, "out")
	os.WriteFile(file, []byte("geotiff bytes"), 0644)

	if !s3put_as(file, "abc", conf) {
		t.Fatal("s3put_as failed")
	}

	if got, want := st.puts, []string{"/geotiff-test/repave/abc"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("put %v, want %v", got, want)
	}

	if !s3has(conf, "abc", int64(len("geotiff bytes"))) {
		t.Error("s3has says the object is not there")
	}

	if s3has(conf, "abc", int64(len("geotiff bytes"))+1) {
		t.Error("s3has accepted the wrong size")
	}
}

func TestRepaveOneWritesToSupabase(t *testing.T) {
	test_tmpdir(t)

	const w, h = 300, 300
	values := smooth(w, h, func(x, y int) float64 { return float64((x / 50) % 3) })
	raster := filepath.Join(tmpdir, "raster.tif")
	write_raster(t, raster, w, h, gdal.Int16, []test_band{{values: values, nodata: f64(-1)}})

	s := serve_files(t, map[string]string{"/r": raster})

	st := &fake_storage{objects: map[string][]byte{}}
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)

	target := supabase_bucket(srv)

	e := run_job(t, repave_job{Endpoint: s.URL + "/r"}, &target)
	if e.Status != repave_done {
		t.Fatalf("%+v", e)
	}

	if want := s3endpoint(target, e.SHA256); e.New != want {
		t.Errorf("new endpoint %s, want %s", e.New, want)
	}

	stored, ok := st.objects["/geotiff-test/repave/"+e.SHA256]
	if !ok {
		t.Fatalf("object not stored: %v", st.puts)
	}

	if int64(len(stored)) != e.NewBytes {
		t.Errorf("stored %d bytes, manifest says %d", len(stored), e.NewBytes)
	}

	written := filepath.Join(tmpdir, "written")
	os.WriteFile(written, stored, 0644)
	if err := rasters_identical(raster, written); err != nil {
		t.Error(err)
	}
}

func TestRepaveSupabaseTargetsOnlyItsOwnSources(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	target := supabase_bucket(srv)
	target.Scratch = false

	// A production raster is not in this bucket, so a job reading it is not
	// this bucket's to write.
	job := repave_job{Endpoint: "https://wri-public-data.s3.amazonaws.com/EnergyAccess/paver-outputs/abc"}

	e := repave_one(job, "key", &target)
	if e.Status != repave_skipped || e.Error != "outside the target bucket" {
		t.Fatalf("%+v", e)
	}

	target.Scratch = true
	if !target.scratch() {
		t.Error("a scratch bucket is not scratch")
	}
}

// The entry documented for the local stack must parse into the fields the
// presigner and s3endpoint read.
func TestS3configSupabaseEntry(t *testing.T) {
	entry := `[
		{
			"name": "local",
			"provider": "supabase",
			"endpoint": "http://127.0.0.1:54332/s3",
			"public": "http://eae.localhost/storage/v1/object/public",
			"bucket": "geotiff-test",
			"directory": "repave",
			"region": "local",
			"key": "access-key-id",
			"secret": "secret-access-key",
			"scratch": true
		}
	]`

	test_tmpdir(t)
	buckets = filepath.Join(tmpdir, "paver-buckets.json")
	if err := os.WriteFile(buckets, []byte(entry), 0644); err != nil {
		t.Fatal(err)
	}

	conf, err := s3config_get("local")
	if err != nil {
		t.Fatal(err)
	}

	if conf.Provider != supabase_provider || conf.Endpoint != "http://127.0.0.1:54332/s3" || !conf.scratch() {
		t.Fatalf("%+v", conf)
	}

	if got, want := s3endpoint(conf, "abc"), "http://eae.localhost/storage/v1/object/public/geotiff-test/repave/abc"; got != want {
		t.Errorf("endpoint %s, want %s", got, want)
	}
}
