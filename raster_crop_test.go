package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

// raster_crop's contract, pinned: the outline raster's grid, its mask, and the
// source's values. These are what the tool assumes when it draws a raster over
// the geography's envelope and combines it with other rasters cell by cell.

func crop_params(t *testing.T) routine_params {
	job, _ := crop_fixture(t)

	return job.routine_params
}

func TestCropTakesTheOutlineRastersGrid(t *testing.T) {
	test_tmpdir(t)

	p := crop_params(t)
	out, err := raster_crop(p.Dataset, p.Base, p.Reference, p.Config, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	if err := crop_on_base_grid(p.Base, out); err != nil {
		t.Fatal(err)
	}

	before, err := gdal.Open(p.Base, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()

	after, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()

	if before.RasterXSize() != after.RasterXSize() || before.GeoTransform() != after.GeoTransform() {
		t.Errorf("grid %dx%d %v", after.RasterXSize(), after.RasterYSize(), after.GeoTransform())
	}
}

// A coarse source covers a cell only partly; the cutline dropped those cells,
// zeros included, and kept cells outside the outline. Both must not happen.
func TestCropKeepsCoarseSourceCellsAndZeros(t *testing.T) {
	test_tmpdir(t)

	p := crop_params(t)

	// 4 km pixels over the outline: a third of them zero, the rest valued.
	coarse := filepath.Join(tmpdir, "coarse.tif")
	write_geographic(t, coarse, 60, 60, func(x, y int) float64 {
		if (x+y)%3 == 0 {
			return 0
		}
		return 10 + float64((x*7+y)%5)
	})
	p.Dataset = coarse

	out, err := raster_crop(p.Dataset, p.Base, p.Reference, p.Config, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	if err := crop_on_base_grid(p.Base, out); err != nil {
		t.Fatal(err)
	}

	ds, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	band := ds.RasterBand(1)
	nodata, _ := band.NoDataValue()
	values := make([]float64, ds.RasterXSize()*ds.RasterYSize())
	if err := band.IO(gdal.Read, 0, 0, ds.RasterXSize(), ds.RasterYSize(), values, ds.RasterXSize(), ds.RasterYSize(), 0, 0); err != nil {
		t.Fatal(err)
	}

	zeros, valued := 0, 0
	for _, v := range values {
		switch {
		case v == nodata:
		case v == 0:
			zeros++
		default:
			valued++
		}
	}

	if valued == 0 {
		t.Fatal("no values")
	}
	if zeros == 0 {
		t.Error("every zero from the source was dropped")
	}
}

func TestCropMatchesTheSourceWhereItIsInTheGridsProjection(t *testing.T) {
	test_tmpdir(t)

	p := crop_params(t)

	// A source already in EPSG:3857, on the outline raster's own grid: each cell
	// must hold its own source value.
	base, err := gdal.Open(p.Base, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	gt := base.GeoTransform()
	width, height := base.RasterXSize(), base.RasterYSize()
	base.Close()

	source := filepath.Join(tmpdir, "grid-source.tif")
	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(source, width, height, 1, gdal.Float32, nil)
	sr := gdal.CreateSpatialReference("")
	sr.FromEPSG(3857)
	wkt, _ := sr.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform(gt)

	values := make([]float32, width*height)
	for i := range values {
		values[i] = float32(100 + (i*37)%17)
	}
	ds.RasterBand(1).IO(gdal.Write, 0, 0, width, height, values, width, height, 0, 0)
	ds.Close()

	p.Dataset = source

	out, err := raster_crop(p.Dataset, p.Base, p.Reference, p.Config, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	agree, err := crop_values_from_source(p.Dataset, p.Base, out, p.Config.Resample)
	if err != nil {
		t.Fatal(err)
	}
	if agree < 0.98 {
		t.Errorf("only %.1f%% of sampled cells match the source", 100*agree)
	}
}

// The mask comes from the outline raster, not from the cutline: cells it leaves
// empty stay empty even where the source has a value.
func TestCropBlanksWhatTheOutlineRasterLeavesEmpty(t *testing.T) {
	test_tmpdir(t)

	p := crop_params(t)

	// Base with a hole: everything left of the middle column is empty.
	holey := filepath.Join(tmpdir, "holey-base.tif")
	drv, _ := gdal.GetDriverByName("GTiff")
	base, _ := gdal.Open(p.Base, gdal.ReadOnly)
	gt := base.GeoTransform()
	width, height := base.RasterXSize(), base.RasterYSize()
	wkt := base.Projection()
	base.Close()

	ds := drv.Create(holey, width, height, 1, gdal.Int16, nil)
	ds.SetProjection(wkt)
	ds.SetGeoTransform(gt)
	band := ds.RasterBand(1)
	band.SetNoDataValue(-1)

	values := make([]int16, width*height)
	for r := 0; r < height; r++ {
		for c := 0; c < width/2; c++ {
			values[r*width+c] = -1
		}
	}
	band.IO(gdal.Write, 0, 0, width, height, values, width, height, 0, 0)
	ds.Close()

	p.Base = holey

	out, err := raster_crop(p.Dataset, p.Base, p.Reference, p.Config, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	if err := crop_on_base_grid(p.Base, out); err != nil {
		t.Fatal(err)
	}

	if outside, err := cells_outside_base_ds(t, holey, out); err != nil {
		t.Fatal(err)
	} else if outside != 0 {
		t.Errorf("%d cells outside the outline raster", outside)
	}
}

func cells_outside_base_ds(t *testing.T, base, out filename) (int, error) {
	t.Helper()

	b, err := gdal.Open(base, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer b.Close()

	o, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer o.Close()

	return cells_outside_base(b, o)
}

// Band 2 carries the area-weighted sum. A source on the grid itself is the
// case where the invariant is exact: each cell holds the source's own value, so
// the sum band's total equals the display band's.
func TestCropSumBandEqualsDisplayForAGridSizedSource(t *testing.T) {
	test_tmpdir(t)

	p := crop_params(t)

	base, err := gdal.Open(p.Base, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	gt, wkt := base.GeoTransform(), base.Projection()
	width, height := base.RasterXSize(), base.RasterYSize()
	base.Close()

	source := filepath.Join(tmpdir, "flat-grid.tif")
	drv, _ := gdal.GetDriverByName("GTiff")
	ds := drv.Create(source, width, height, 1, gdal.Float32, nil)
	ds.SetProjection(wkt)
	ds.SetGeoTransform(gt)

	flat := make([]float32, width*height)
	for i := range flat {
		flat[i] = 4
	}
	ds.RasterBand(1).IO(gdal.Write, 0, 0, width, height, flat, width, height, 0, 0)
	ds.Close()

	p.Dataset = source

	out, err := raster_crop(p.Dataset, p.Base, p.Reference, p.Config, noop_reporter)
	if err != nil {
		t.Fatal(err)
	}

	ds2, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ds2.Close()

	near := total_of_band(t, ds2, 1)
	sum := total_of_band(t, ds2, 2)

	if near == 0 {
		t.Fatal("band 1 is empty")
	}
	if math.Abs(sum-near) > near*0.05 {
		t.Errorf("band 2 total %.1f against band 1's %.1f", sum, near)
	}
}

func total_of_band(t *testing.T, ds gdal.Dataset, i int) float64 {
	t.Helper()

	band := ds.RasterBand(i)
	nodata, has := band.NoDataValue()
	width, height := ds.RasterXSize(), ds.RasterYSize()

	values := make([]float64, width*height)
	if err := band.IO(gdal.Read, 0, 0, width, height, values, width, height, 0, 0); err != nil {
		t.Fatal(err)
	}

	total := 0.0
	for _, v := range values {
		if math.IsNaN(v) || (has && v == nodata) {
			continue
		}
		total += v
	}

	return total
}
