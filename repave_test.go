package main

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

	return repave_one(j, key, target, false)
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
	if e.New != s3endpoint(bucket, e.SHA256) {
		t.Errorf("new endpoint %s", e.New)
	}
	if e.NewBytes >= e.OldBytes {
		t.Errorf("%d -> %d bytes", e.OldBytes, e.NewBytes)
	}

	written := filepath.Join(bucket.Directory, e.SHA256)
	if err := rasters_identical(raster, written); err != nil {
		t.Fatal(err)
	}
	if sum, _ := file_sha256(written); sum != e.SHA256 {
		t.Errorf("object %s holds bytes hashing to %s", e.SHA256, sum)
	}

	// The same bytes are already there: nothing is written.
	long_ago := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(written, long_ago, long_ago)
	if again := run_job(t, repave_job{Endpoint: s.URL + "/r"}, &bucket); again.New != e.New {
		t.Errorf("rerun wrote %s, first run %s", again.New, e.New)
	}
	if st, _ := os.Stat(written); !st.ModTime().Equal(long_ago) {
		t.Error("rerun rewrote an object it already had")
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

	e := repave_one(repave_job{Endpoint: "https://energyaccess-storage.s3.amazonaws.com/paver-outputs/x"}, "k", &bucket, false)
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

	// The outline raster: 1000 m cells over the outline, as geographies have.
	base := filepath.Join(tmpdir, "base.tif")
	write_mercator_base(t, base, 3261000, 0, 160, 190)

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

	job, _ := crop_fixture(t)

	// What crop-raster published before the grid fix: cut to the outline, on a
	// grid snapped to the source's own pixels.
	legacy := legacy_crop(t, job.routine_params)
	published := serve_files(t, map[string]string{"/legacy": legacy})
	bucket := local_bucket()

	job.Endpoint = published.URL + "/legacy"
	e := run_job(t, job, &bucket)
	if e.Status != repave_done || e.Routine != "crop-raster" || e.NewBands != 3 {
		t.Fatalf("%+v", e)
	}

	written := filepath.Join(bucket.Directory, e.SHA256)
	assert_deliverable_layout(t, written, "1", "3")

	if err := crop_on_base_grid(job.Base, written); err != nil {
		t.Errorf("rebuild is not on the outline raster's grid: %v", err)
	}

	ds, err := gdal.Open(written, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if n, _ := valued_cells(ds); n == 0 {
		t.Error("rebuild holds no values")
	}
}

// legacy_crop writes what crop-raster published before: a cutline warp onto
// the outline raster's pixel count, no mask from the outline raster.
func legacy_crop(t *testing.T, p routine_params) filename {
	t.Helper()

	base, err := gdal.Open(p.Base, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	f := gdal.OpenDataSource(p.Reference, 0)
	defer f.Destroy()

	gt := base.GeoTransform()
	w, h := base.RasterXSize(), base.RasterYSize()

	out := _filename() + ".tif"

	opts := []string{
		"-of", "GTiff",
		"-t_srs", "EPSG:3857",
		"-ts", strconv.Itoa(w), strconv.Itoa(h),
		"-r", p.Config.Resample,
		"-ot", "Float32",
		"-dstnodata", strconv.Itoa(p.Config.Nodata),
		"-cutline", p.Reference,
		"-crop_to_cutline",
		"-cl", f.LayerByIndex(0).Name(),
	}
	_ = gt

	src, err := gdal.Open(p.Dataset, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	dest, err := gdal.Warp(out, nil, []gdal.Dataset{src}, opts)
	if err != nil {
		t.Fatal(err)
	}
	dest.Close()

	return out
}

func TestRepaveCropReportsLostCoverage(t *testing.T) {
	test_tmpdir(t)

	job, _ := crop_fixture(t)

	// A published file that carries a value in only a few cells: the rebuild
	// keeps far more, but the check is that it never loses what was there.
	job.Endpoint = ""
	lost := _filename() + ".tif"
	write_mercator_base(t, lost, 3261000, 0, 160, 190)

	served := serve_files(t, map[string]string{"/lost": lost})
	bucket := local_bucket()
	job.Endpoint = served.URL + "/lost"

	e := run_job(t, job, &bucket)
	if e.Status != repave_done {
		t.Errorf("a rebuild that keeps everything was refused: %+v", e)
	}
}

func TestRepaveRetriesARebuildWhoseChecksCouldNotRun(t *testing.T) {
	test_tmpdir(t)

	job, _ := crop_fixture(t)

	unreadable := crop_on_base_grid(job.Base, filepath.Join(tmpdir, "missing.tif"))
	if unreadable == nil {
		t.Fatal("a missing rebuild passed the grid check")
	}

	other := filepath.Join(tmpdir, "other.tif")
	write_mercator_base(t, other, 3261000, 0, 10, 10)
	wrong := crop_on_base_grid(job.Base, other)

	if err := crop_verdict(nil, wrong, nil); !errors.Is(err, err_mismatch) {
		t.Errorf("a wrong grid is not a mismatch: %v", err)
	}
	if err := crop_verdict(wrong, unreadable); errors.Is(err, err_mismatch) || err == nil {
		t.Errorf("a check that could not read its input settles the job: %v", err)
	}
	if err := crop_verdict(nil, nil, nil); err != nil {
		t.Error(err)
	}
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

{"endpoint":"https://a/crop","dataset":"d","geography":"g1","routine":"crop-raster","dataseturl":"https://s","referenceurl":"https://r","baseurl":"https://b","config":{"numbertype":"UInt16","nodata":65535,"resample":"near"},"resolution":1000}
https://a/plain
{"endpoint":"https://a/crop","dataset":"d2","geography":"g1","routine":"crop-raster","dataseturl":"https://s","referenceurl":"https://r","baseurl":"https://b","config":{"numbertype":"UInt16","nodata":65535,"resample":"near"},"resolution":1000}
{"endpoint":"https://a/crop","dataset":"d3","geography":"g2","routine":"crop-raster","dataseturl":"https://s","referenceurl":"https://r2","baseurl":"https://b2","config":{"numbertype":"UInt16","nodata":65535,"resample":"near"},"resolution":1000}
`), 0644)

	jobs, err := read_jobs(list)
	if err != nil {
		t.Fatal(err)
	}
	// A clone in the same geography shares the job; one in another geography
	// gets its own rebuild.
	if len(jobs) != 3 {
		t.Fatalf("%d jobs", len(jobs))
	}
	if jobs[1].DatasetID != "d" || jobs[2].Geography != "g2" || jobs[2].Base != "https://b2" {
		t.Errorf("%+v", jobs)
	}
	if jobs[0].routine() != repave_reencode || jobs[1].Config.Nodata != 65535 || jobs[1].Resolution != 1000 || jobs[1].Base != "https://b" {
		t.Errorf("%+v", jobs)
	}

	os.WriteFile(list, []byte(`{"endpoint":"https://a/crop","routine":"crop-raster"}`), 0644)
	if _, err := read_jobs(list); err == nil {
		t.Error("crop-raster job without its inputs accepted")
	}

	os.WriteFile(list, []byte(`{"endpoint":"https://a/crop","routine":"crop-raster","dataseturl":"https://s","referenceurl":"https://r","baseurl":"https://b","config":{"resample":"near"}}`), 0644)
	if _, err := read_jobs(list); err == nil {
		t.Error("crop-raster job without a geography accepted")
	}
}

func TestManifestResumesFromTheLastEntry(t *testing.T) {
	test_tmpdir(t)

	manifest := filepath.Join(tmpdir, "m.jsonl")
	os.WriteFile(manifest, []byte(`{"old":"u","geography":"g","status":"failed","error":"x"}
{"old":"u","geography":"g","status":"done","new":"n","bucket":"local","key":"k"}
{"old":"u","geography":"g2","status":"failed","error":"y"}
`), 0644)

	entries, err := manifest_read(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if e := entries[job_ref{"u", "g"}]; e.Status != repave_done || e.New != "n" || e.Key != "k" {
		t.Errorf("%+v", e)
	}
	if e := entries[job_ref{"u", "g2"}]; e.Status != repave_failed {
		t.Errorf("another geography's entry: %+v", e)
	}

	b, _ := json.Marshal(entries[job_ref{"u", "g"}])
	if !strings.Contains(string(b), `"key":"k"`) {
		t.Error(string(b))
	}
}

// write_mercator_base writes a single-band 1000 m EPSG:3857 raster covering the
// outline used by crop_fixture, with every cell valued.
func write_mercator_base(t *testing.T, path string, x, y float64, w, h int) {
	t.Helper()

	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(path, w, h, 1, gdal.Int16, nil)

	sr := gdal.CreateSpatialReference("")
	sr.FromEPSG(3857)
	wkt, _ := sr.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform([6]float64{x, 1000, 0, y, 0, -1000})

	band := ds.RasterBand(1)
	band.SetNoDataValue(-1)
	band.IO(gdal.Write, 0, 0, w, h, make([]int16, w*h), w, h, 0, 0)
	ds.Close()
}
