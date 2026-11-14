package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

func serve_files(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, f)
	}))
	t.Cleanup(s.Close)

	return s
}

func local_bucket() s3config {
	return s3config{Name: "local", Provider: local_provider, Bucket: "http://test.localhost/repave", Directory: filepath.Join(tmpdir, "bucket")}
}

func run_job(t *testing.T, j repave_job, target *s3config) repave_entry {
	t.Helper()

	key, err := repave_key(j)
	if err != nil {
		t.Fatal(err)
	}

	return repave_one(j, key, target)
}

func TestRepaveReencodesUnderNewKeys(t *testing.T) {
	test_tmpdir(t)

	const w, h = 500, 500
	values := smooth(w, h, func(x, y int) float64 { return float64((x / 50) % 3) })
	raster := filepath.Join(tmpdir, "raster.tif")
	write_raster(t, raster, w, h, gdal.Int16, []test_band{{values: values, nodata: f64(-1)}})

	text := filepath.Join(tmpdir, "vectors.geojson")
	os.WriteFile(text, []byte(`{"type":"FeatureCollection","features":[]}`), 0644)

	s := serve_files(t, map[string]string{"/r": raster, "/v": text})
	bucket := local_bucket()

	e := run_job(t, repave_job{Endpoint: s.URL + "/r"}, &bucket)
	if e.Status != repave_done || e.Routine != repave_reencode {
		t.Fatalf("%+v", e)
	}
	if e.New == e.Old || !strings.HasPrefix(e.New, "http://test.localhost/repave/") {
		t.Errorf("new endpoint %s", e.New)
	}
	if e.NewBytes >= e.OldBytes {
		t.Errorf("%d -> %d bytes", e.OldBytes, e.NewBytes)
	}

	written := filepath.Join(bucket.Directory, _uuid(e.New))
	if err := rasters_identical(raster, written); err != nil {
		t.Fatal(err)
	}

	if v := run_job(t, repave_job{Endpoint: s.URL + "/v"}, &bucket); v.Status != repave_skipped {
		t.Errorf("non-GeoTIFF: %+v", v)
	}
	if _, err := repave_key(repave_job{Endpoint: s.URL + "/missing"}); err == nil {
		t.Error("missing object has a key")
	}

	if d := run_job(t, repave_job{Endpoint: s.URL + "/r"}, nil); d.Status != repave_verified || d.New != "" {
		t.Errorf("dry run: %+v", d)
	}

	again := serve_files(t, map[string]string{"/w": written})
	if u := run_job(t, repave_job{Endpoint: again.URL + "/w"}, &bucket); u.Status != repave_unchanged {
		t.Errorf("re-encoding a deliverable: %+v", u)
	}
}

func TestRepaveRefusesEndpointsOutsideAnS3Bucket(t *testing.T) {
	test_tmpdir(t)

	bucket := s3config{Name: "world", Provider: "s3.amazonaws.com", Bucket: "wri-public-data", Directory: "EnergyAccess/paver-outputs"}

	e := repave_one(repave_job{Endpoint: "https://energyaccess-storage.s3.amazonaws.com/paver-outputs/x"}, "k", &bucket)
	if e.Status != repave_skipped || e.Bucket != "world" {
		t.Errorf("%+v", e)
	}
}

// crop_fixture serves what admin hands crop-raster: a Float32 source raster
// in EPSG:4326, an outline polygon, and the outline raster whose size the
// output takes.
func crop_fixture(t *testing.T) (repave_job, *httptest.Server) {
	t.Helper()

	source := filepath.Join(tmpdir, "source.tif")
	write_geographic(t, source, 300, 300, func(x, y int) float64 {
		return 100 + 50*math.Sin(float64(x)/20)*math.Cos(float64(y)/30)
	})

	outline := filepath.Join(tmpdir, "outline.geojson")
	os.WriteFile(outline, []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},
		"geometry":{"type":"Polygon","coordinates":[[[29.3,-1.7],[30.6,-1.6],[30.7,-0.3],[29.4,-0.4],[29.3,-1.7]]]}}]}`), 0644)

	base := filepath.Join(tmpdir, "base.tif")
	write_raster(t, base, 140, 150, gdal.Int16, []test_band{{values: make([]float64, 140*150), nodata: f64(-1)}})

	s := serve_files(t, map[string]string{"/source.tif": source, "/outline.geojson": outline, "/base.tif": base})

	return repave_job{
		DatasetID: "dataset-id",
		Routine:   "crop-raster",
		routine_params: routine_params{
			Dataset:    s.URL + "/source.tif",
			Reference:  s.URL + "/outline.geojson",
			Base:       s.URL + "/base.tif",
			Resolution: 1000,
			Config:     raster_config{Numbertype: "Float32", Nodata: -32767, Resample: "near"},
		},
	}, s
}

func write_geographic(t *testing.T, path string, width, height int, f func(x, y int) float64) {
	t.Helper()

	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(path, width, height, 1, gdal.Float32, nil)

	sr := gdal.CreateSpatialReference("")
	sr.FromEPSG(4326)
	wkt, _ := sr.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform([6]float64{29, 2.0 / float64(width), 0, 0, 0, -2.0 / float64(height)})

	values := smooth(width, height, f)
	s := make([]float32, len(values))
	for i, v := range values {
		s[i] = float32(v)
	}
	ds.RasterBand(1).IO(gdal.Write, 0, 0, width, height, s, width, height, 0, 0)
	ds.Close()
}

// legacy_from writes band 1 of a three-band output as the single-band raster
// crop-raster used to publish.
func legacy_from(t *testing.T, three filename, out filename, edit func(band gdal.RasterBand)) {
	t.Helper()

	if b, err := exec.Command("gdal_translate", "-q", "-b", "1", three, out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}

	if edit != nil {
		ds, err := gdal.Open(out, gdal.Update)
		if err != nil {
			t.Fatal(err)
		}
		edit(ds.RasterBand(1))
		ds.Close()
	}
}

func TestRepaveRebuildsLegacyRastersFromTheirSource(t *testing.T) {
	test_tmpdir(t)

	job, s := crop_fixture(t)

	current, err := crop_raster_output(noop_reporter, job.routine_params)
	if err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(tmpdir, "legacy.tif")
	legacy_from(t, current, legacy, nil)

	changed := filepath.Join(tmpdir, "changed.tif")
	legacy_from(t, current, changed, func(b gdal.RasterBand) {
		v := []float32{0}
		b.IO(gdal.Read, 70, 75, 1, 1, v, 1, 1, 0, 0)
		v[0] += 1
		b.IO(gdal.Write, 70, 75, 1, 1, v, 1, 1, 0, 0)
	})

	published := serve_files(t, map[string]string{"/legacy": legacy, "/changed": changed, "/current": current})
	bucket := local_bucket()

	job.Endpoint = published.URL + "/legacy"
	e := run_job(t, job, &bucket)
	if e.Status != repave_done || e.Routine != "crop-raster" || e.OldBands != 1 || e.NewBands != 3 {
		t.Fatalf("%+v", e)
	}

	written := filepath.Join(bucket.Directory, _uuid(e.New))
	assert_deliverable_layout(t, written, "1", "3")
	if err := rasters_identical(current, written); err != nil {
		t.Errorf("rebuild is not what crop-raster publishes: %v", err)
	}

	job.Endpoint = published.URL + "/changed"
	if m := run_job(t, job, &bucket); m.Status != repave_mismatch || !strings.Contains(m.Error, "1 of") {
		t.Errorf("a changed display value: %+v", m)
	}

	job.Endpoint = published.URL + "/current"
	if u := run_job(t, job, &bucket); u.Status != repave_unchanged {
		t.Errorf("rebuilding what is already current: %+v", u)
	}

	_ = s
}

func TestRepaveKeyFollowsEveryInput(t *testing.T) {
	test_tmpdir(t)

	job, s := crop_fixture(t)

	legacy := filepath.Join(tmpdir, "legacy.tif")
	write_raster(t, legacy, 10, 10, gdal.Int16, []test_band{{values: make([]float64, 100)}})
	published := serve_files(t, map[string]string{"/legacy": legacy})
	job.Endpoint = published.URL + "/legacy"

	key := func(j repave_job) string {
		k, err := repave_key(j)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	k := key(job)
	if key(job) != k {
		t.Fatal("key is not stable")
	}

	config := job
	config.Config.Resample = "average"
	if key(config) == k {
		t.Error("key ignores the config")
	}

	reencode := job
	reencode.Routine = ""
	if key(reencode) == k {
		t.Error("key ignores the routine")
	}

	write_raster(t, filepath.Join(tmpdir, "source.tif"), 300, 300, gdal.Float32, []test_band{{values: make([]float64, 90000)}})
	if key(job) == k {
		t.Error("key ignores a changed source")
	}

	_ = s
}

func TestRepaveSettled(t *testing.T) {
	cases := []struct {
		e      repave_entry
		bucket string
		key    string
		want   bool
	}{
		{repave_entry{Key: "k", Status: repave_done, Bucket: "local"}, "local", "k", true},
		{repave_entry{Key: "k", Status: repave_done, Bucket: "local"}, "local", "k2", false},
		{repave_entry{Key: "k", Status: repave_done, Bucket: "local"}, "world", "k", false},
		{repave_entry{Key: "k", Status: repave_verified}, "", "k", true},
		{repave_entry{Key: "k", Status: repave_verified}, "world", "k", false},
		{repave_entry{Key: "k", Status: repave_unchanged}, "world", "k", true},
		{repave_entry{Key: "k", Status: repave_mismatch}, "world", "k", true},
		{repave_entry{Key: "k", Status: repave_skipped, Bucket: "world"}, "india", "k", false},
		{repave_entry{Key: "k", Status: repave_failed}, "", "k", false},
	}

	for _, c := range cases {
		if got := repave_settled(c.e, c.bucket, c.key); got != c.want {
			t.Errorf("settled(%+v, %q, %q) = %v", c.e, c.bucket, c.key, got)
		}
	}
}

func TestReadJobs(t *testing.T) {
	test_tmpdir(t)

	list := filepath.Join(tmpdir, "jobs")
	os.WriteFile(list, []byte(`https://a/plain

{"endpoint":"https://a/crop","dataset":"d","routine":"crop-raster","dataseturl":"https://s","referenceurl":"https://r","baseurl":"https://b","config":{"numbertype":"UInt16","nodata":65535,"resample":"near"},"resolution":1000}
https://a/plain
`), 0644)

	jobs, err := read_jobs(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d jobs", len(jobs))
	}
	if jobs[0].routine() != repave_reencode || jobs[1].Config.Nodata != 65535 || jobs[1].Resolution != 1000 || jobs[1].Base != "https://b" {
		t.Errorf("%+v", jobs)
	}

	os.WriteFile(list, []byte(`{"endpoint":"https://a/crop","routine":"crop-raster"}`), 0644)
	if _, err := read_jobs(list); err == nil {
		t.Error("crop-raster job without its inputs accepted")
	}
}

func TestRepaveSqlRepointsAndRollsBack(t *testing.T) {
	test_tmpdir(t)

	manifest := filepath.Join(tmpdir, "m.jsonl")
	entries := map[string]repave_entry{
		"https://a/old":  {Old: "https://a/old", New: "https://a/new", Status: repave_done},
		"https://a/o'q":  {Old: "https://a/o'q", New: "https://a/n", Status: repave_done},
		"https://a/same": {Old: "https://a/same", Status: repave_unchanged},
		"https://a/diff": {Old: "https://a/diff", Status: repave_mismatch},
	}

	if err := repave_sql(entries, manifest); err != nil {
		t.Fatal(err)
	}

	apply, _ := os.ReadFile(manifest + ".apply.sql")
	rollback, _ := os.ReadFile(manifest + ".rollback.sql")

	if n := strings.Count(string(apply), "update datasets"); n != 2 {
		t.Errorf("%d updates in apply", n)
	}
	if !strings.Contains(string(apply), `'https://a/o''q'`) {
		t.Error("quote not escaped")
	}
	if !strings.Contains(string(rollback), "f->>'endpoint' = 'https://a/new' then jsonb_set(f, '{endpoint}', to_jsonb('https://a/old'::text))") {
		t.Errorf("rollback:\n%s", rollback)
	}
	if !strings.HasPrefix(string(apply), "begin;\n") || !strings.HasSuffix(string(apply), "commit;\n") {
		t.Error("apply is not one transaction")
	}
}

func TestManifestResumesFromTheLastEntry(t *testing.T) {
	test_tmpdir(t)

	manifest := filepath.Join(tmpdir, "m.jsonl")
	os.WriteFile(manifest, []byte(`{"old":"u","status":"failed","error":"x"}
{"old":"u","status":"done","new":"n","bucket":"local","key":"k"}
`), 0644)

	entries, err := manifest_read(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if e := entries["u"]; e.Status != repave_done || e.New != "n" || e.Key != "k" {
		t.Errorf("%+v", e)
	}

	b, _ := json.Marshal(entries["u"])
	if !strings.Contains(string(b), `"key":"k"`) {
		t.Error(string(b))
	}
}
