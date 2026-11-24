package main

// infer.go's two halves: reading what a routine published, and choosing the
// parameters to try. The comparisons are tested on files written here; the
// candidates are tested end to end, by letting the routine publish once and
// asking infer what it was run with.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

// write_geojson writes a FeatureCollection of the given features.
func write_geojson(t *testing.T, path string, features ...string) filename {
	t.Helper()

	doc := `{"type":"FeatureCollection","features":[` + strings.Join(features, ",") + `]}`
	if err := os.WriteFile(path, []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}

	return path
}

func point_feature(properties, coordinates string) string {
	return `{"type":"Feature","properties":` + properties + `,"geometry":{"type":"Point","coordinates":` + coordinates + `}}`
}

func TestFeaturesReadPastAHeaderObject(t *testing.T) {
	test_tmpdir(t)

	f := write_geojson(t, filepath.Join(tmpdir, "crs-first.geojson"),
		point_feature(`{"id":1}`, `[1,2]`), point_feature(`{"id":2}`, `[3,4]`))

	// ogr2ogr writes a crs object before features; its closing brace is not the
	// document's.
	doc := `{"type":"FeatureCollection","crs":{"type":"name","properties":{"name":"urn:ogc:def:crs:OGC:1.3:CRS84"}},` +
		strings.TrimPrefix(string(must_read(t, f)), `{"type":"FeatureCollection",`)

	if err := os.WriteFile(f, []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}

	stream, err := open_features(f)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()

	n := 0
	for {
		_, err := stream.next()
		if err != nil {
			break
		}
		n++
	}

	if n != 2 {
		t.Fatalf("%d features, want 2", n)
	}
}

func must_read(t *testing.T, f filename) []byte {
	t.Helper()

	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestVectorsCompareSaysHowItDiffers(t *testing.T) {
	test_tmpdir(t)

	published := write_geojson(t, filepath.Join(tmpdir, "published.geojson"),
		point_feature(`{"a":"x","b":1}`, `[1.0000001,2]`),
		point_feature(`{"a":"y","b":2}`, `[3,4]`))

	same := write_geojson(t, filepath.Join(tmpdir, "same.geojson"),
		point_feature(`{"a":"x","b":1}`, `[1.0000001,2]`),
		point_feature(`{"a":"y","b":2}`, `[3,4]`))

	if status, note := vectors_compare(published, same); status != infer_reproduced {
		t.Fatalf("same: %s %s", status, note)
	}

	// A key order and a coordinate's last bits are not a difference.
	drifted := write_geojson(t, filepath.Join(tmpdir, "drifted.geojson"),
		`{"geometry":{"coordinates":[1.0000004,2],"type":"Point"},"properties":{"b":1,"a":"x"},"type":"Feature"}`,
		point_feature(`{"a":"y","b":2}`, `[3,4]`))

	if status, note := vectors_compare(published, drifted); status != infer_equivalent {
		t.Fatalf("drifted: %s %s", status, note)
	}

	reordered := write_geojson(t, filepath.Join(tmpdir, "reordered.geojson"),
		point_feature(`{"a":"y","b":2}`, `[3,4]`),
		point_feature(`{"a":"x","b":1}`, `[1.0000001,2]`))

	if status, note := vectors_compare(published, reordered); status != infer_equivalent {
		t.Fatalf("reordered: %s %s", status, note)
	}

	renamed := write_geojson(t, filepath.Join(tmpdir, "renamed.geojson"),
		point_feature(`{"a":"x","bb":1}`, `[1.0000001,2]`),
		point_feature(`{"a":"y","bb":2}`, `[3,4]`))

	status, note := vectors_compare(published, renamed)
	if status != infer_differs {
		t.Fatalf("renamed: %s %s", status, note)
	}
	if !strings.Contains(note, "properties") || !strings.Contains(note, "bb") {
		t.Fatalf("renamed note does not name the property: %s", note)
	}

	moved := write_geojson(t, filepath.Join(tmpdir, "moved.geojson"),
		point_feature(`{"a":"x","b":1}`, `[11,2]`),
		point_feature(`{"a":"y","b":2}`, `[3,4]`))

	if status, note := vectors_compare(published, moved); status != infer_differs || !strings.Contains(note, "geometry") {
		t.Fatalf("moved: %s %s", status, note)
	}
}

func TestCsvCompareOrdersRows(t *testing.T) {
	test_tmpdir(t)

	published := filepath.Join(tmpdir, "published.csv")
	os.WriteFile(published, []byte("id,value\n1,2\n3,4\n"), 0644)

	if status, note := csv_compare(pathf(tmpdir, "same.csv", "id,value\n1,2\n3,4\n"), published); status != infer_reproduced {
		t.Fatalf("same: %s %s", status, note)
	}

	if status, note := csv_compare(pathf(tmpdir, "order.csv", "id,value\n3,4\n1,2\n"), published); status != infer_equivalent {
		t.Fatalf("reordered: %s %s", status, note)
	}

	if status, note := csv_compare(pathf(tmpdir, "value.csv", "id,value\n1,9\n3,4\n"), published); status != infer_differs {
		t.Fatalf("changed: %s %s", status, note)
	}
}

func pathf(dir, name, content string) filename {
	f := filepath.Join(dir, name)
	os.WriteFile(f, []byte(content), 0644)
	return f
}

func TestInferClipProximityCandidates(t *testing.T) {
	test_tmpdir(t)

	published := map[string]filename{"vectors": write_geojson(t, filepath.Join(tmpdir, "published.geojson"),
		point_feature(`{"b":1,"a":"x"}`, `[1,2]`))}

	simplify := float32(0.0005)

	j := infer_job{Routine: "clip-proximity", Type: "lines", Resolution: 1000, Simplify: &simplify}

	candidates, err := infer_candidates(j, "source.geojson", "reference.geojson", "", published)
	if err != nil {
		t.Fatal(err)
	}

	// The dialog's defaults first: the category's simplification, undissolved.
	want := []struct {
		simplify float32
		dissolve bool
	}{
		{0.0005, false},
		{0.0005, true},
		{0, false},
		{0, true},
	}

	if len(candidates) <= len(want) {
		t.Fatalf("%d candidates, want more than %d", len(candidates), len(want))
	}

	for i, w := range want {
		if candidates[i].params.Simplify != w.simplify || candidates[i].params.Dissolve != w.dissolve {
			t.Fatalf("candidate %d: simplify %g dissolve %t, want %g/%t",
				i, candidates[i].params.Simplify, candidates[i].params.Dissolve, w.simplify, w.dissolve)
		}
	}

	legacy := false
	for _, c := range candidates[len(want):] {
		legacy = legacy || c.params.Simplify == legacy_simplify
	}
	if !legacy {
		t.Fatal("paver's own simplification of the reference was not tried")
	}

	last := candidates[len(candidates)-1]
	if last.params.Simplify == 0 || last.params.Simplify == 0.0005 || last.params.Simplify == legacy_simplify {
		t.Fatalf("the grid-sized fallback was not tried: %g", last.params.Simplify)
	}

	if got := candidates[0].params.Fields; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("fields %v, want the published properties sorted", got)
	}
}

func TestInferClipProximityNeverDissolvesPoints(t *testing.T) {
	test_tmpdir(t)

	published := map[string]filename{"vectors": write_geojson(t, filepath.Join(tmpdir, "published.geojson"),
		point_feature(`{"a":"x"}`, `[1,2]`))}

	candidates, err := infer_candidates(infer_job{Routine: "clip-proximity", Type: "points"}, "source", "reference", "", published)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range candidates {
		if c.params.Dissolve {
			t.Fatal("a points dataset was tried dissolved")
		}
	}
}

func TestInferParamsAreTheRoutinesOwnNames(t *testing.T) {
	got := routine_params_map("crop-raster", routine_params{
		Dataset:    "/tmp/source",
		Reference:  "/tmp/outline",
		Base:       "/tmp/base",
		Resolution: 1000,
		Config:     raster_config{Numbertype: "Uint16", Nodata: 0, Resample: "near"},
	})

	b, _ := json.Marshal(got)
	if string(b) != `{"config":{"numbertype":"Uint16","nodata":0,"resample":"near"},"resolution":1000}` {
		t.Fatalf("crop-raster params %s", b)
	}

	got = routine_params_map("clip-proximity", routine_params{
		Dataset:  "/tmp/source",
		Fields:   []string{"a", "b"},
		Dissolve: true,
		Simplify: 0.0005,
	})

	b, _ = json.Marshal(got)
	if string(b) != `{"dissolve":true,"fields":["a","b"],"resolution":0,"simplify":0.0005}` {
		t.Fatalf("clip-proximity params %s", b)
	}
}

func TestInferSettledRetriesWhatCouldNotRun(t *testing.T) {
	settled := []string{infer_reproduced, infer_equivalent}

	for _, status := range settled {
		if !infer_settled(infer_entry{Key: "k", Check: infer_check{Status: status}}, "k") {
			t.Fatalf("%s with its key is not settled", status)
		}
	}

	for _, status := range []string{infer_differs, infer_failed, infer_unchecked, ""} {
		if infer_settled(infer_entry{Key: "k", Check: infer_check{Status: status}}, "k") {
			t.Fatalf("%s with its key is settled", status)
		}
	}

	if infer_settled(infer_entry{Key: "other", Check: infer_check{Status: infer_reproduced}}, "k") {
		t.Fatal("a settled entry from another input set is settled")
	}
}

func TestInferOriginalsFollowsAMovedReference(t *testing.T) {
	test_tmpdir(t)

	f := filepath.Join(tmpdir, "repoints.jsonl")
	os.WriteFile(f, []byte(strings.Join([]string{
		`{"dataset":"d","from":"a","to":"b","status":"applied"}`,
		`{"dataset":"d","from":"b","to":"c","status":"applied"}`,
		`{"dataset":"other","from":"a","to":"z","status":"restored"}`,
	}, "\n")), 0644)

	resolve, err := infer_originals(f)
	if err != nil {
		t.Fatal(err)
	}

	if got := resolve("d", "c"); got != "a" {
		t.Fatalf("c resolves to %s, want a", got)
	}
	if got := resolve("other", "z"); got != "z" {
		t.Fatalf("a restored pair resolves to %s, want z", got)
	}
	if got := resolve("d", "b"); got != "a" {
		t.Fatalf("b resolves to %s, want a", got)
	}
}

func TestInferReadJobsRefusesWhatItCannotRun(t *testing.T) {
	test_tmpdir(t)

	good := `{"dataset":"d","routine":"crop-raster","outputs":{"raster":"http://x/y"}}`

	for _, bad := range []string{
		`{"dataset":"d","routine":"no-such-routine","outputs":{"raster":"http://x/y"}}`,
		`{"dataset":"d","routine":"crop-raster"}`,
		`{"routine":"crop-raster","outputs":{"raster":"http://x/y"}}`,
	} {
		f := pathf(tmpdir, "jobs.jsonl", bad)
		if _, err := infer_read_jobs(f); err == nil {
			t.Fatalf("read %s without complaint", bad)
		}
	}

	jobs, err := infer_read_jobs(pathf(tmpdir, "good.jsonl", good))
	if err != nil {
		t.Fatal(err)
	}

	if len(jobs) != 1 || jobs[0].Routine != "crop-raster" {
		t.Fatalf("jobs %+v", jobs)
	}
}

// The routines publish once with known parameters; infer has to arrive at the
// same ones from the files alone.
func TestInferReproducesAClipThatKeptNoFields(t *testing.T) {
	test_tmpdir(t)

	source := write_geojson(t, filepath.Join(tmpdir, "source.geojson"),
		`{"type":"Feature","properties":{"name":"a","value":1},"geometry":{"type":"LineString","coordinates":[[29.5,-1.2],[29.9,-1.4]]}}`,
		`{"type":"Feature","properties":{"name":"b","value":2},"geometry":{"type":"LineString","coordinates":[[29.6,-1.1],[30.0,-1.3]]}}`)

	reference := write_geojson(t, filepath.Join(tmpdir, "reference.geojson"),
		`{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[29.0,-2.0],[30.5,-2.0],[30.5,-0.5],[29.0,-0.5],[29.0,-2.0]]]}}`)

	j := infer_job{Routine: "clip-proximity", Type: "lines", Resolution: 1000, DatasetURL: source, Reference: reference}

	published, err := infer_run(clip_proximity_2021, routine_params{
		Dataset:    source,
		Reference:  reference,
		Resolution: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	params, check := infer_settle(j, source, reference, "", published)

	if check.Status != infer_reproduced || !strings.Contains(check.Note, "2021") {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	b, _ := json.Marshal(params)
	if string(b) != `{"dissolve":false,"fields":[],"resolution":1000,"simplify":0}` {
		t.Fatalf("params %s", b)
	}
}

func TestInferReproducesClipProximity(t *testing.T) {
	test_tmpdir(t)

	source := write_geojson(t, filepath.Join(tmpdir, "source.geojson"),
		`{"type":"Feature","properties":{"name":"a","value":1},"geometry":{"type":"LineString","coordinates":[[29.5,-1.2],[29.9,-1.4]]}}`,
		`{"type":"Feature","properties":{"name":"b","value":2},"geometry":{"type":"LineString","coordinates":[[29.6,-1.1],[30.0,-1.3]]}}`)

	reference := write_geojson(t, filepath.Join(tmpdir, "reference.geojson"),
		`{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[29.0,-2.0],[30.5,-2.0],[30.5,-0.5],[29.0,-0.5],[29.0,-2.0]]]}}`)

	j := infer_job{Routine: "clip-proximity", Type: "lines", Resolution: 1000, DatasetURL: source, Reference: reference}

	published, err := infer_run("clip-proximity", routine_params{
		Dataset:    source,
		Reference:  reference,
		Resolution: 1000,
		Fields:     []string{"name", "value"},
		Simplify:   0,
	})
	if err != nil {
		t.Fatal(err)
	}

	params, check := infer_settle(j, source, reference, "", published)

	if check.Status != infer_reproduced {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	b, _ := json.Marshal(params)
	if string(b) != `{"dissolve":false,"fields":["name","value"],"resolution":1000,"simplify":0}` {
		t.Fatalf("params %s", b)
	}
}

func TestInferReproducesCsvPoints(t *testing.T) {
	test_tmpdir(t)

	source := pathf(tmpdir, "source.csv", "station,lng,lat,name\n1,29.5,-1.2,a\n2,29.7,-1.9,b\n")
	reference := write_geojson(t, filepath.Join(tmpdir, "reference.geojson"),
		`{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[29.0,-2.0],[30.5,-2.0],[30.5,-0.5],[29.0,-0.5],[29.0,-2.0]]]}}`)

	published, err := infer_run("csv-points", routine_params{
		Dataset:    source,
		Reference:  reference,
		Resolution: 1000,
		LngLat:     [2]string{"lng", "lat"},
		Fields:     []string{"name", "station"},
	})
	if err != nil {
		t.Fatal(err)
	}

	j := infer_job{Routine: "csv-points", Type: "points", Resolution: 1000}

	params, check := infer_settle(j, source, reference, "", published)

	if check.Status != infer_reproduced {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	b, _ := json.Marshal(params)
	if string(b) != `{"fields":["name","station"],"lnglat":["lng","lat"],"resolution":1000}` {
		t.Fatalf("params %s", b)
	}
}

func TestInferReproducesCsvRaster(t *testing.T) {
	test_tmpdir(t)

	source := pathf(tmpdir, "source.csv", "station,lng,lat,value\n1,29.5,-1.2,7\n2,29.7,-1.9,9\n3,29.9,-1.3,7\n")
	reference := write_geojson(t, filepath.Join(tmpdir, "reference.geojson"),
		`{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[29.0,-2.0],[30.5,-2.0],[30.5,-0.5],[29.0,-0.5],[29.0,-2.0]]]}}`)

	published, err := infer_run("csv-raster", routine_params{
		Dataset:    source,
		Reference:  reference,
		Resolution: 1000,
		LngLat:     [2]string{"lng", "lat"},
		Attr:       "value",
	})
	if err != nil {
		t.Fatal(err)
	}

	j := infer_job{Routine: "csv-raster", Type: "raster", Resolution: 1000}

	params, check := infer_settle(j, source, reference, "", published)

	if check.Status != infer_reproduced {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	b, _ := json.Marshal(params)
	if string(b) != `{"attr":"value","lnglat":["lng","lat"],"resolution":1000}` {
		t.Fatalf("params %s", b)
	}
}

func TestInferFindsIdsWrittenAsDigits(t *testing.T) {
	test_tmpdir(t)

	vectors := write_geojson(t, filepath.Join(tmpdir, "published.geojson"),
		point_feature(`{"name":"a","code":"RW1","pcode":"1101"}`, `[1,2]`),
		point_feature(`{"name":"b","code":"RW2","pcode":"1102"}`, `[3,4]`))

	unique, err := geojson_unique_numbers(vectors)
	if err != nil {
		t.Fatal(err)
	}

	if len(unique) != 1 || unique[0] != "pcode" {
		t.Fatalf("unique numbers %v, want [pcode]", unique)
	}
}

func TestInferReproducesAdminBoundaries(t *testing.T) {
	test_tmpdir(t)

	source := write_geojson(t, filepath.Join(tmpdir, "source.geojson"),
		`{"type":"Feature","properties":{"gid":1,"name":"a"},"geometry":{"type":"Polygon","coordinates":[[[29.2,-1.8],[29.6,-1.8],[29.6,-1.4],[29.2,-1.4],[29.2,-1.8]]]}}`,
		`{"type":"Feature","properties":{"gid":2,"name":"b"},"geometry":{"type":"Polygon","coordinates":[[[29.8,-1.8],[30.2,-1.8],[30.2,-1.4],[29.8,-1.4],[29.8,-1.8]]]}}`)

	published, err := infer_run("admin-boundaries", routine_params{
		Dataset:    source,
		Resolution: 1000,
		Attr:       "gid",
	})
	if err != nil {
		t.Fatal(err)
	}

	j := infer_job{Routine: "admin-boundaries", Type: "polygons-boundaries", Resolution: 1000,
		Vectors: json.RawMessage(`{"vectors_id":"gid"}`)}

	params, check := infer_settle(j, source, "", "", published)

	if check.Status != infer_reproduced {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	b, _ := json.Marshal(params)
	if string(b) != `{"attr":"gid","resolution":1000}` {
		t.Fatalf("params %s", b)
	}
}

func TestInferReproducesARasterAnEarlierCropMade(t *testing.T) {
	test_tmpdir(t)

	job, _ := crop_fixture(t)
	p := job.routine_params

	for _, crop := range []string{crop_raster_2021, crop_raster_2026} {
		published, err := infer_run(crop, p)
		if err != nil {
			t.Fatal(err)
		}

		inferred := infer_job{Routine: "crop-raster", Resolution: p.Resolution, Config: p.Config}

		_, check := infer_settle(inferred, p.Dataset, p.Reference, p.Base, published)

		if check.Status != infer_reproduced || check.Candidates != 1 {
			t.Fatalf("%s: %s after %d candidates, %s", crop, check.Status, check.Candidates, check.Note)
		}

		if !strings.Contains(check.Note, crop[len("crop-raster@"):]) {
			t.Fatalf("%s: settled as %q", crop, check.Note)
		}

		infer_trash(published)
	}
}

func TestInferReproducesACropRaster(t *testing.T) {
	test_tmpdir(t)

	job, _ := crop_fixture(t)
	p := job.routine_params

	published, err := infer_run("crop-raster", p)
	if err != nil {
		t.Fatal(err)
	}

	inferred := infer_job{Routine: "crop-raster", Resolution: p.Resolution, Config: p.Config}

	params, check := infer_settle(inferred, p.Dataset, p.Reference, p.Base, published)

	if check.Status != infer_reproduced {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	if params["resolution"] != 1000 {
		t.Fatalf("params %+v", params)
	}

	if _, ok := params["config"]; !ok {
		t.Fatalf("params %+v has no config", params)
	}
}

func TestInferSettlesOnTheClosestCandidate(t *testing.T) {
	test_tmpdir(t)

	published := map[string]filename{"raster": filepath.Join(tmpdir, "published.tif")}

	const w, h = 20, 20

	write_raster(t, published["raster"], w, h, gdal.Int16, []test_band{
		{values: smooth(w, h, func(x, y int) float64 { return float64(x + y) }), nodata: f64(-1)},
	})

	j := infer_job{Routine: "admin-boundaries", Resolution: 1000,
		Vectors: json.RawMessage(`{"vectors_id":"missing"}`)}

	// No source to run, and the published raster is not one the routine makes:
	// the job fails rather than claiming a fit.
	_, check := infer_settle(j, "", "", "", published)

	if check.Status != infer_failed {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}
}

func TestInferMatchesACropOnItsOwnGrid(t *testing.T) {
	test_tmpdir(t)

	crop_fixture(t)
	source, outline, base := filepath.Join(tmpdir, "source.tif"), filepath.Join(tmpdir, "outline.geojson"), filepath.Join(tmpdir, "base.tif")

	category := raster_config{Numbertype: "Float32", Nodata: -32767, Resample: "bilinear"}

	made := map[string]func(raster_config) (filename, error){
		"2021": func(rc raster_config) (filename, error) { return raster_crop_2021(source, base, outline, rc, 1000) },
		"2026": func(rc raster_config) (filename, error) { return raster_crop_2026(source, base, outline, rc) },
		// The EAE-500 re-pave: band one as the 2021 crop made it, beside the 2026 sums.
		"re-pave": func(rc raster_config) (filename, error) {
			sums, err := raster_crop_2026(source, base, outline, rc)
			if err != nil {
				return "", err
			}

			g, err := read_published_grid(sums)
			if err != nil {
				return "", err
			}

			first, err := warp_on_grid_2021(source, outline, g, rc.Resample, raster_config{Numbertype: "Float32", Nodata: rc.Nodata}, 1000)
			if err != nil {
				return "", err
			}

			return stack_bands(t, []filename{first, sums, sums}, []int{1, 2, 3}), nil
		},
	}

	for name, crop := range made {
		then := raster_config{Numbertype: "Int16", Nodata: -9999, Resample: "bilinear"}

		published, err := crop(then)
		if err != nil {
			t.Fatal(err)
		}

		config, check := infer_crop_on_grid(source, outline, published, category, 1000)

		if check.Status != infer_matched {
			t.Fatalf("%s: %s: %s", name, check.Status, check.Note)
		}

		if config.Nodata != -9999 || config.Resample != "bilinear" {
			t.Fatalf("%s: config %+v", name, config)
		}

		if name == "2021" && config.Numbertype != "Int16" {
			t.Fatalf("%s: type %s, want the published Int16", name, config.Numbertype)
		}

		trash(published)
	}
}

func TestInferDoesNotMatchACropWithDataTheOutlineHasNone(t *testing.T) {
	test_tmpdir(t)

	crop_fixture(t)
	source, outline := filepath.Join(tmpdir, "source.tif"), filepath.Join(tmpdir, "outline.geojson")
	base := filepath.Join(tmpdir, "base.tif")

	config := raster_config{Numbertype: "Float32", Nodata: -9999, Resample: "near"}

	published, err := raster_crop_2026(source, base, outline, config)
	if err != nil {
		t.Fatal(err)
	}
	defer trash(published)

	// A block inside the country that production left without data, as a
	// crop with other parameters would have.
	ds, err := gdal.Open(published, gdal.Update)
	if err != nil {
		t.Fatal(err)
	}
	block := make([]float32, 100)
	for i := range block {
		block[i] = -9999
	}
	for b := 1; b <= 3; b++ {
		ds.RasterBand(b).IO(gdal.Write, 75, 85, 10, 10, block, 10, 10, 0, 0)
	}
	ds.Close()

	_, check := infer_crop_on_grid(source, outline, published, config, 1000)

	if check.Status == infer_matched {
		t.Fatalf("matched: %s", check.Note)
	}
	if check.Profile == nil || check.Profile.Interior <= grid_interior_share || check.Profile.Edge != 0 {
		t.Fatalf("profile %+v: %s", check.Profile, check.Note)
	}
}

func TestVectorsCompareTakesPartsInAnyOrder(t *testing.T) {
	test_tmpdir(t)

	line := func(parts string) string {
		return `{"type":"Feature","properties":{"a":1},"geometry":{"type":"MultiLineString","coordinates":` + parts + `}}`
	}

	published := write_geojson(t, filepath.Join(tmpdir, "published.geojson"), line(`[[[1,2],[3,4]],[[5,6],[7,8]]]`))
	rebuilt := write_geojson(t, filepath.Join(tmpdir, "rebuilt.geojson"), line(`[[[5,6],[7,8]],[[1,2],[3,4]]]`))

	if status, note := vectors_compare(published, rebuilt); status != infer_equivalent {
		t.Fatalf("%s: %s", status, note)
	}

	reversed := write_geojson(t, filepath.Join(tmpdir, "reversed.geojson"), line(`[[[3,4],[1,2]],[[5,6],[7,8]]]`))

	if status, _ := vectors_compare(published, reversed); status != infer_differs {
		t.Fatalf("a reversed line is %s", status)
	}
}

func TestInferLeavesNothingBehind(t *testing.T) {
	test_tmpdir(t)

	crop_fixture(t)
	source, outline, base := filepath.Join(tmpdir, "source.tif"), filepath.Join(tmpdir, "outline.geojson"), filepath.Join(tmpdir, "base.tif")

	category := raster_config{Numbertype: "Float32", Nodata: -32767, Resample: "near"}

	published, err := raster_crop_2026(source, base, outline, raster_config{Numbertype: "Float32", Nodata: -9999, Resample: "near"})
	if err != nil {
		t.Fatal(err)
	}

	// One cell off, as another GDAL's crop would be: only the grid matches it.
	ds, err := gdal.Open(published, gdal.Update)
	if err != nil {
		t.Fatal(err)
	}
	cell := []float32{0}
	band := ds.RasterBand(1)
	band.IO(gdal.Read, 80, 90, 1, 1, cell, 1, 1, 0, 0)
	cell[0] += 1000
	band.IO(gdal.Write, 80, 90, 1, 1, cell, 1, 1, 0, 0)
	ds.Close()

	before, _ := os.ReadDir(tmpdir)

	j := infer_job{Routine: "crop-raster", Resolution: 1000, Config: category}
	if _, check := infer_settle(j, source, outline, base, map[string]filename{"raster": published}); check.Status != infer_matched {
		t.Fatalf("%s: %s", check.Status, check.Note)
	}

	after, _ := os.ReadDir(tmpdir)
	if len(after) != len(before) {
		names := []string{}
		for _, e := range after {
			names = append(names, e.Name())
		}
		t.Fatalf("%d files before, %d after: %v", len(before), len(after), names)
	}
}

// stack_bands writes a GeoTIFF of band bands[i] of files[i], in order.
func stack_bands(t *testing.T, files []filename, bands []int) filename {
	t.Helper()

	singles := []filename{}
	for i, f := range files {
		one := _filename() + ".tif"
		if b, err := exec.Command("gdal_translate", "-q", "-b", strconv.Itoa(bands[i]), f, one).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		singles = append(singles, one)
	}

	vrt := _filename() + ".vrt"
	if b, err := exec.Command("gdalbuildvrt", append([]string{"-q", "-separate", vrt}, singles...)...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}

	out := _filename()
	if b, err := exec.Command("gdal_translate", "-q", vrt, out).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}

	return out
}

func TestVectorsCompareTakesARingFromAnyVertex(t *testing.T) {
	test_tmpdir(t)

	polygon := func(rings string) string {
		return `{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":` + rings + `}}`
	}

	published := write_geojson(t, filepath.Join(tmpdir, "published.geojson"),
		polygon(`[[[0,0],[4,0],[4,4],[0,4],[0,0]],[[1,1],[1,2],[2,2],[1,1]],[[3,3],[3,3.5],[3.5,3.5],[3,3]]]`))
	rebuilt := write_geojson(t, filepath.Join(tmpdir, "rebuilt.geojson"),
		polygon(`[[[4,4],[0,4],[0,0],[4,0],[4,4]],[[3,3.5],[3.5,3.5],[3,3],[3,3.5]],[[1,2],[2,2],[1,1],[1,2]]]`))

	if status, note := vectors_compare(published, rebuilt); status != infer_equivalent {
		t.Fatalf("%s: %s", status, note)
	}

	holes_out := write_geojson(t, filepath.Join(tmpdir, "holes.geojson"),
		polygon(`[[[1,1],[1,2],[2,2],[1,1]],[[0,0],[4,0],[4,4],[0,4],[0,0]],[[3,3],[3,3.5],[3.5,3.5],[3,3]]]`))

	if status, _ := vectors_compare(published, holes_out); status != infer_differs {
		t.Fatalf("a hole taken for the outer ring is %s", status)
	}
}

func TestRastersEquivalentAllowCellsAtTheOutlinesEdge(t *testing.T) {
	test_tmpdir(t)

	// Data in the left half, nodata (-1) in the right; 0.01% of it is 4 cells.
	outline := func(x, y int) float64 {
		if x < 100 {
			return float64(x + y)
		}
		return -1
	}

	published := filepath.Join(tmpdir, "published.tif")
	write_mercator_int16(t, published, 200, 200, outline)

	// Two cells side by side, two and three cells off the edge, as along DR
	// Congo's border with Burundi.
	edge := filepath.Join(tmpdir, "edge.tif")
	write_mercator_int16(t, edge, 200, 200, func(x, y int) float64 {
		if (x == 101 || x == 102) && y == 40 {
			return 91
		}
		return outline(x, y)
	})

	if edges, err := rasters_equivalent(published, edge); err != nil || edges != 2 {
		t.Fatalf("a patch off the edge: %d edges, %v", edges, err)
	}

	inland := filepath.Join(tmpdir, "inland.tif")
	write_mercator_int16(t, inland, 200, 200, func(x, y int) float64 {
		if x == 160 && y == 40 {
			return 120
		}
		return outline(x, y)
	})

	if _, err := rasters_equivalent(published, inland); err == nil {
		t.Fatal("a cell sixty cells off the edge was taken for the edge")
	}
}

func write_mercator_int16(t *testing.T, path string, width, height int, value func(x, y int) float64) {
	t.Helper()

	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(path, width, height, 1, gdal.Int16, nil)
	defer ds.Close()

	srs := gdal.CreateSpatialReference("")
	srs.FromEPSG(3857)
	wkt, _ := srs.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform([6]float64{3200000, 1000, 0, -100000, 0, -1000})

	band := ds.RasterBand(1)
	band.SetNoDataValue(-1)

	row := make([]int16, width)
	for y := 0; y < height; y++ {
		for x := range row {
			row[x] = int16(value(x, y))
		}
		band.IO(gdal.Write, 0, y, width, 1, row, width, 1, 0, 0)
	}
}

func TestVectorsClipInChunksWhateverTheFeatureIds(t *testing.T) {
	test_tmpdir(t)

	if min(runtime.NumCPU(), 8) < 2 {
		t.Skip("clips in chunks only with more than one CPU")
	}

	features := []string{}
	for i := 0; i < 2500; i++ {
		x, y := 29.5+float64(i%50)*0.01, -1.5+float64(i/50)*0.01
		features = append(features, fmt.Sprintf(`{"type":"Feature","id":%d,"properties":{"n":%d},"geometry":{"type":"Point","coordinates":[%g,%g]}}`, i+11, i, x, y))
	}
	points := write_geojson(t, filepath.Join(tmpdir, "points.geojson"), features...)

	outline := write_geojson(t, filepath.Join(tmpdir, "outline.geojson"),
		`{"type":"Feature","properties":{},"geometry":{"type":"Polygon","coordinates":[[[29.0,-2.0],[30.5,-2.0],[30.5,-0.5],[29.0,-0.5],[29.0,-2.0]]]}}`)

	clipped, err := vectors_clip(points, outline, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	ds := gdal.OpenDataSource(clipped, 0)
	defer ds.Destroy()

	if n, _ := ds.LayerByIndex(0).FeatureCount(true); n != 2500 {
		t.Fatalf("%d features clipped, want 2500", n)
	}
}
