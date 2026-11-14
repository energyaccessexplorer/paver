package main

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

// The rebuild checks read rasters a strip at a time. Whole-raster versions of
// the same checks are the reference they must agree with.

func write_grid(t *testing.T, path string, gt [6]float64, w, h int, nodata *float64, f func(x, y int) float64) {
	t.Helper()

	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(path, w, h, 1, gdal.Float32, []string{"TILED=YES"})

	sr := gdal.CreateSpatialReference("")
	sr.FromEPSG(3857)
	wkt, _ := sr.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform(gt)

	band := ds.RasterBand(1)
	if nodata != nil {
		band.SetNoDataValue(*nodata)
	}

	values := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			values[y*w+x] = float32(f(x, y))
		}
	}
	if err := band.IO(gdal.Write, 0, 0, w, h, values, w, h, 0, 0); err != nil {
		t.Fatal(err)
	}
	ds.Close()
}

func read_whole(t *testing.T, ds gdal.Dataset) ([]float64, func(float64) bool) {
	t.Helper()

	band := ds.RasterBand(1)
	nodata, has := band.NoDataValue()
	w, h := ds.RasterXSize(), ds.RasterYSize()

	values := make([]float64, w*h)
	if err := band.IO(gdal.Read, 0, 0, w, h, values, w, h, 0, 0); err != nil {
		t.Fatal(err)
	}

	return values, func(v float64) bool { return math.IsNaN(v) || (has && v == nodata) }
}

func whole_valued_inside(t *testing.T, ds, base gdal.Dataset) int {
	mask, masked := read_whole(t, base)
	values, empty := read_whole(t, ds)

	bgt, gt := base.GeoTransform(), ds.GeoTransform()
	bw, bh := base.RasterXSize(), base.RasterYSize()
	w, h := ds.RasterXSize(), ds.RasterYSize()

	count := 0
	for r := 0; r < h; r++ {
		y := gt[3] + (float64(r)+0.5)*gt[5]
		for c := 0; c < w; c++ {
			if empty(values[r*w+c]) {
				continue
			}
			x := gt[0] + (float64(c)+0.5)*gt[1]
			bc := int(math.Floor((x - bgt[0]) / bgt[1]))
			br := int(math.Floor((y - bgt[3]) / bgt[5]))
			if bc < 0 || bc >= bw || br < 0 || br >= bh || masked(mask[br*bw+bc]) {
				continue
			}
			count++
		}
	}

	return count
}

func whole_values_from_source(t *testing.T, s, b, o gdal.Dataset) float64 {
	source, source_empty := read_whole(t, s)
	out, out_empty := read_whole(t, o)

	sgt, gt := s.GeoTransform(), b.GeoTransform()
	sw, sh := s.RasterXSize(), s.RasterYSize()
	bw, bh := b.RasterXSize(), b.RasterYSize()

	checked, got := 0, 0
	for i := 0; i < bw*bh && checked < 2000; i += 7 {
		r, c := (i/bw)%bh, i%bw
		v := out[r*bw+c]
		if out_empty(v) {
			continue
		}

		x := gt[0] + (float64(c)+0.5)*gt[1]
		y := gt[3] + (float64(r)+0.5)*gt[5]
		sc := int(math.Floor((x - sgt[0]) / sgt[1]))
		sr := int(math.Floor((y - sgt[3]) / sgt[5]))
		if sc < 0 || sc >= sw || sr < 0 || sr >= sh {
			continue
		}

		matched := false
		for dr := -1; dr <= 1 && !matched; dr++ {
			for dc := -1; dc <= 1 && !matched; dc++ {
				r2, c2 := sr+dr, sc+dc
				if r2 < 0 || r2 >= sh || c2 < 0 || c2 >= sw || source_empty(source[r2*sw+c2]) {
					continue
				}
				matched = v == source[r2*sw+c2]
			}
		}

		checked++
		if matched {
			got++
		}
	}

	if checked < 100 {
		return 1
	}

	return float64(got) / float64(checked)
}

func open_ro(t *testing.T, f filename) gdal.Dataset {
	t.Helper()

	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ds.Close)

	return ds
}

// Grids that line up with the outline raster, and ones that do not: shifted,
// coarser, finer, reaching past it.
var check_grids = []struct {
	name string
	gt   [6]float64
	w, h int
}{
	{"same", [6]float64{3000000, 1000, 0, -500000, 0, -1000}, 120, 90},
	{"shifted", [6]float64{3000300, 1000, 0, -500450, 0, -1000}, 120, 90},
	{"coarser", [6]float64{2990000, 2500, 0, -495000, 0, -2500}, 52, 40},
	{"finer", [6]float64{2980000, 400, 0, -480000, 0, -400}, 360, 280},
}

func TestStripedChecksAgreeWithWholeReads(t *testing.T) {
	test_tmpdir(t)

	keep := strip_cells
	t.Cleanup(func() { strip_cells = keep })

	base := filepath.Join(tmpdir, "base.tif")
	write_grid(t, base, check_grids[0].gt, 120, 90, f64(-1), func(x, y int) float64 {
		if (x+2*y)%7 == 0 || x < 3 {
			return -1
		}
		return 1
	})

	for _, g := range check_grids {
		ds := filepath.Join(tmpdir, g.name+".tif")
		write_grid(t, ds, g.gt, g.w, g.h, f64(-9999), func(x, y int) float64 {
			switch {
			case (x*y)%11 == 0:
				return -9999
			case (x+y)%13 == 0:
				return math.NaN()
			}
			return float64((x*31 + y*17) % 23)
		})

		// The output of a crop on the base grid: the source's values with
		// some cells altered and some empty.
		out := filepath.Join(tmpdir, g.name+"-out.tif")
		src := open_ro(t, ds)
		sv, _ := read_whole(t, src)
		sgt := src.GeoTransform()
		write_grid(t, out, check_grids[0].gt, 120, 90, f64(-9999), func(x, y int) float64 {
			cx := 3000000 + (float64(x)+0.5)*1000
			cy := -500000 - (float64(y)+0.5)*1000
			sc := int(math.Floor((cx - sgt[0]) / sgt[1]))
			sr := int(math.Floor((cy - sgt[3]) / sgt[5]))
			switch {
			case sc < 0 || sc >= g.w || sr < 0 || sr >= g.h, (x+y)%17 == 0:
				return -9999
			case (x+3*y)%19 == 0:
				return 777
			case (x+5*y)%3 == 0 && sc+1 < g.w:
				// Nearest may take a neighbour whose area holds the centre.
				return sv[sr*g.w+sc+1]
			case (2*x+y)%5 == 0 && sr+1 < g.h:
				return sv[(sr+1)*g.w+sc]
			}
			return sv[sr*g.w+sc]
		})

		for _, cells := range []int{1 << 21, 1, 333, 1000} {
			strip_cells = cells
			name := fmt.Sprintf("%s/strip_cells=%d", g.name, cells)

			b := open_ro(t, base)
			want := whole_valued_inside(t, open_ro(t, ds), b)
			got, err := valued_inside(open_ro(t, ds), b)
			if err != nil || got != want {
				t.Errorf("%s: valued_inside %d (%v), whole read %d", name, got, err, want)
			}
			if want == 0 {
				t.Errorf("%s: fixture counts nothing", name)
			}

			wantv := whole_values_from_source(t, open_ro(t, ds), b, open_ro(t, out))
			gotv, err := crop_values_from_source(ds, base, out, "near")
			if err != nil || gotv != wantv {
				t.Errorf("%s: crop_values_from_source %v (%v), whole read %v", name, gotv, err, wantv)
			}
			if wantv == 1 || wantv < 0.5 {
				t.Errorf("%s: fixture agreement %v exercises nothing", name, wantv)
			}
		}
	}
}

func allocated(f func()) uint64 {
	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

// A whole-raster read of these 3000x3000 grids is 72 MB a raster.
func TestRepaveChecksReadInStrips(t *testing.T) {
	test_tmpdir(t)

	const n = 3000
	gt := [6]float64{3000000, 100, 0, -500000, 0, -100}

	base := filepath.Join(tmpdir, "base.tif")
	out := filepath.Join(tmpdir, "out.tif")
	published := filepath.Join(tmpdir, "published.tif")
	for _, f := range []string{base, out, published} {
		write_grid(t, f, gt, n, n, f64(-1), func(x, y int) float64 { return float64((x + y) % 5) })
	}

	const limit = 40 << 20

	var err error
	b, o := open_ro(t, base), open_ro(t, out)
	if a := allocated(func() { _, err = valued_inside(o, b) }); a > limit || err != nil {
		t.Errorf("valued_inside allocated %d MB (%v)", a>>20, err)
	}

	var agree float64
	if a := allocated(func() { agree, err = crop_values_from_source(published, base, out, "near") }); a > limit || err != nil || agree != 1 {
		t.Errorf("crop_values_from_source allocated %d MB, agree %v (%v)", a>>20, agree, err)
	}
}
