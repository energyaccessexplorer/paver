package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

// write_points writes a GeoJSON of n points whose FIDs start at 1, as
// GeoJSON sources carrying their own "id" member do.
func write_points(t *testing.T, n int) filename {
	features := make([]map[string]any, n)
	for i := range features {
		features[i] = map[string]any{
			"type":       "Feature",
			"id":         i + 1,
			"properties": map[string]any{"gid": i + 1},
			"geometry": map[string]any{
				"type":        "Point",
				"coordinates": []float64{30 + float64(i)*1e-4, 0},
			},
		}
	}

	return write_geojson(t, "points.geojson", features)
}

func write_container(t *testing.T) filename {
	return write_geojson(t, "container.geojson", []map[string]any{{
		"type":       "Feature",
		"properties": map[string]any{},
		"geometry": map[string]any{
			"type":        "Polygon",
			"coordinates": [][][]float64{{{29, -1}, {31, -1}, {31, 1}, {29, 1}, {29, -1}}},
		},
	}})
}

func write_geojson(t *testing.T, name string, features []map[string]any) filename {
	b, err := json.Marshal(map[string]any{"type": "FeatureCollection", "features": features})
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0644); err != nil {
		t.Fatal(err)
	}

	return p
}

func gids(t *testing.T, in filename) map[int64]bool {
	ds := gdal.OpenDataSource(in, 0)
	defer ds.Destroy()

	l := ds.LayerByIndex(0)
	seen := map[int64]bool{}

	l.ResetReading()
	for f := l.NextFeature(); f != nil; f = l.NextFeature() {
		seen[f.FieldAsInteger64(f.FieldIndex("gid"))] = true
		f.Destroy()
	}

	return seen
}

func nop_reporter(string, ...any) string { return "" }

func TestVectorsClipParallel_OneBasedFIDs(t *testing.T) {
	tmpdir = t.TempDir()

	const n = 2500
	out, err := vectors_clip_parallel(write_points(t, n), write_container(t), 4, nop_reporter)
	if err != nil {
		t.Fatalf("clip failed: %v", err)
	}

	seen := gids(t, out)
	for i := int64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("feature gid=%d missing from the clip (got %d of %d)", i, len(seen), n)
		}
	}
}

func TestVectorsFeaturesSplit_OneBasedFIDs(t *testing.T) {
	tmpdir = t.TempDir()

	const n = 5
	r, err := vectors_features_split(write_points(t, n), "gid", nop_reporter)
	if err != nil {
		t.Fatalf("split failed: %v", err)
	}

	for i := int64(1); i <= n; i++ {
		if _, ok := r[i]; !ok {
			t.Fatalf("feature gid=%d missing from the split: %v", i, r)
		}
	}
}
