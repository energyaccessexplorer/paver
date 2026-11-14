package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

type test_band struct {
	values []float64
	nodata *float64
	interp gdal.ColorInterp
}

func f64(v float64) *float64 { return &v }

// write_raster writes an uncompressed, pixel-interleaved, one-row-strip
// GeoTIFF: the layout paver's writers left behind.
func write_raster(t *testing.T, path string, width, height int, dt gdal.DataType, bands []test_band) {
	t.Helper()

	drv, err := gdal.GetDriverByName("GTiff")
	if err != nil {
		t.Fatal(err)
	}

	ds := drv.Create(path, width, height, len(bands), dt, []string{"INTERLEAVE=PIXEL", "BLOCKYSIZE=1"})

	sr := gdal.CreateSpatialReference("")
	if err := sr.FromEPSG(3857); err != nil {
		t.Fatal(err)
	}
	wkt, _ := sr.ToWKT()
	ds.SetProjection(wkt)
	ds.SetGeoTransform([6]float64{3000000, 1000, 0, -500000, 0, -1000})

	for i, b := range bands {
		band := ds.RasterBand(i + 1)
		if b.nodata != nil {
			band.SetNoDataValue(*b.nodata)
		}
		if b.interp != 0 {
			band.SetColorInterp(b.interp)
		}

		var buf any
		switch dt {
		case gdal.Byte:
			s := make([]uint8, len(b.values))
			for k, v := range b.values {
				s[k] = uint8(v)
			}
			buf = s
		case gdal.Int16:
			s := make([]int16, len(b.values))
			for k, v := range b.values {
				s[k] = int16(v)
			}
			buf = s
		case gdal.UInt32:
			s := make([]uint32, len(b.values))
			for k, v := range b.values {
				s[k] = uint32(v)
			}
			buf = s
		case gdal.Float32:
			s := make([]float32, len(b.values))
			for k, v := range b.values {
				s[k] = float32(v)
			}
			buf = s
		default:
			t.Fatalf("write_raster: unsupported type %v", dt)
		}

		if err := band.IO(gdal.Write, 0, 0, width, height, buf, width, height, 0, 0); err != nil {
			t.Fatal(err)
		}
	}

	ds.Close()
}

type gdalinfo_json struct {
	Size     []int                        `json:"size"`
	Metadata map[string]map[string]string `json:"metadata"`
	Bands    []struct {
		Block []int  `json:"block"`
		Type  string `json:"type"`
	} `json:"bands"`
}

func gdalinfo(t *testing.T, path string) gdalinfo_json {
	t.Helper()

	out, err := exec.Command("gdalinfo", "-json", path).Output()
	if err != nil {
		t.Fatal(err)
	}

	var j gdalinfo_json
	if err := json.Unmarshal(out, &j); err != nil {
		t.Fatal(err)
	}

	return j
}

func assert_deliverable_layout(t *testing.T, path string, predictors ...string) {
	t.Helper()

	j := gdalinfo(t, path)
	is := j.Metadata["IMAGE_STRUCTURE"]

	if is["COMPRESSION"] != "DEFLATE" {
		t.Errorf("COMPRESSION = %q, want DEFLATE", is["COMPRESSION"])
	}
	if is["INTERLEAVE"] != "BAND" {
		t.Errorf("INTERLEAVE = %q, want BAND", is["INTERLEAVE"])
	}

	predictor := is["PREDICTOR"]
	if predictor == "" {
		predictor = "1"
	}
	ok := false
	for _, p := range predictors {
		ok = ok || p == predictor
	}
	if !ok {
		t.Errorf("PREDICTOR = %s, want one of %v", predictor, predictors)
	}

	rows := min(deliverable_strip_rows, j.Size[1])
	for i, b := range j.Bands {
		if len(b.Block) != 2 || b.Block[0] != j.Size[0] || b.Block[1] != rows {
			t.Errorf("band %d: block %v, want %dx%d strips", i+1, b.Block, j.Size[0], rows)
		}
	}
}

func test_tmpdir(t *testing.T) {
	t.Helper()
	tmpdir = t.TempDir()
}

func smooth(width, height int, f func(x, y int) float64) []float64 {
	out := make([]float64, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			out[y*width+x] = f(x, y)
		}
	}
	return out
}

func TestDeliverableFloatBandsKeepEverySampleBit(t *testing.T) {
	test_tmpdir(t)

	const w, h = 700, 530
	near := smooth(w, h, func(x, y int) float64 { return math.Sin(float64(x)/50) * math.Cos(float64(y)/40) * 1000 })
	near[0], near[1], near[2] = math.NaN(), math.Copysign(0, -1), -32767
	near[w*h-1] = math.Inf(1)
	sum := smooth(w, h, func(x, y int) float64 { return float64(x*y) / 7 })
	avg := smooth(w, h, func(x, y int) float64 { return float64(x+y) / 3 })

	in := filepath.Join(tmpdir, "in.tif")
	write_raster(t, in, w, h, gdal.Float32, []test_band{
		{values: near, nodata: f64(-32767), interp: gdal.CI_GrayIndex},
		{values: sum, nodata: f64(-32767)},
		{values: avg, nodata: f64(-32767)},
	})

	out, predictor, err := raster_deliverable(in)
	if err != nil {
		t.Fatal(err)
	}

	if predictor != "3" {
		t.Errorf("smooth float field chose predictor %s, want 3", predictor)
	}
	assert_deliverable_layout(t, out, "3")

	if err := rasters_identical(in, out); err != nil {
		t.Fatal(err)
	}
	if file_size(out) >= file_size(in) {
		t.Errorf("deliverable %d bytes, source %d", file_size(out), file_size(in))
	}
}

func TestDeliverableCategoricalIntegersSkipThePredictor(t *testing.T) {
	test_tmpdir(t)

	const w, h = 600, 600
	r := rand.New(rand.NewSource(520))
	values := make([]float64, w*h)
	for i := range values {
		values[i] = float64(r.Intn(4) * 1000)
	}

	in := filepath.Join(tmpdir, "in.tif")
	write_raster(t, in, w, h, gdal.Int16, []test_band{{values: values, nodata: f64(-1)}})

	out, predictor, err := raster_deliverable(in)
	if err != nil {
		t.Fatal(err)
	}

	if predictor != "1" {
		t.Errorf("categorical integers chose predictor %s, want 1", predictor)
	}
	assert_deliverable_layout(t, out, "1")
}

func TestDeliverableAlphaAndUnsignedBands(t *testing.T) {
	test_tmpdir(t)

	const w, h = 300, 200
	values := smooth(w, h, func(x, y int) float64 { return float64(x*1000 + y) })
	alpha := smooth(w, h, func(x, y int) float64 {
		if x < 20 {
			return 0
		}
		return 255
	})

	in := filepath.Join(tmpdir, "in.tif")
	write_raster(t, in, w, h, gdal.UInt32, []test_band{
		{values: values, interp: gdal.CI_GrayIndex},
		{values: alpha, interp: gdal.CI_AlphaBand},
	})

	out, _, err := raster_deliverable(in)
	if err != nil {
		t.Fatal(err)
	}

	assert_deliverable_layout(t, out, "1", "2")

	ds, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if ds.RasterBand(2).ColorInterp() != gdal.CI_AlphaBand {
		t.Error("alpha band lost its colour interpretation")
	}
}

func TestRastersIdenticalCatchesEveryDifference(t *testing.T) {
	test_tmpdir(t)

	const w, h = 64, 300
	base := smooth(w, h, func(x, y int) float64 { return float64(x + y) })

	write := func(name string, values []float64, nodata float64) string {
		p := filepath.Join(tmpdir, name)
		write_raster(t, p, w, h, gdal.Float32, []test_band{{values: values, nodata: f64(nodata)}})
		return p
	}

	a := write("a.tif", base, -1)

	if err := rasters_identical(a, write("same.tif", base, -1)); err != nil {
		t.Fatalf("identical rasters reported different: %v", err)
	}

	sample := append([]float64(nil), base...)
	sample[w*290+3] += 0.5
	if err := rasters_identical(a, write("sample.tif", sample, -1)); err == nil || !strings.Contains(err.Error(), "rows 256..") {
		t.Errorf("one changed sample in the last row block: %v", err)
	}

	zero := append([]float64(nil), base...)
	zero[0] = math.Copysign(0, -1)
	if err := rasters_identical(a, write("zero.tif", zero, -1)); err == nil {
		t.Error("+0 and -0 reported identical")
	}

	if err := rasters_identical(a, write("nodata.tif", base, -2)); err == nil {
		t.Error("different nodata reported identical")
	}
}

func TestCleanupDeliversRastersAndLeavesVectorsAlone(t *testing.T) {
	test_tmpdir(t)

	const w, h = 400, 400
	values := smooth(w, h, func(x, y int) float64 {
		if (x-200)*(x-200)+(y-200)*(y-200) < 900 {
			return 1
		}
		return 0
	})

	raster := _filename()
	write_raster(t, raster, w, h, gdal.Int16, []test_band{{values: values, nodata: f64(-1)}})
	reference := _filename() + ".tif"
	copy_file(t, raster, reference)

	vectors := _filename()
	geojson := []byte(`{"type":"FeatureCollection","features":[]}`)
	if err := os.WriteFile(vectors, geojson, 0644); err != nil {
		t.Fatal(err)
	}

	bucket := s3config{Name: "local", Provider: local_provider, Bucket: "http://test.localhost/out/", Directory: filepath.Join(tmpdir, "bucket")}

	if err := cleanup([]filename{raster, vectors}, nil, noopReporter, bucket); err != nil {
		t.Fatal(err)
	}

	published := filepath.Join(bucket.Directory, _uuid(raster))
	assert_deliverable_layout(t, published, "1", "2")
	if err := rasters_identical(reference, published); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(bucket.Directory, _uuid(vectors)))
	if err != nil || string(got) != string(geojson) {
		t.Errorf("vectors published as %q (%v)", got, err)
	}

	if url := s3endpoint(bucket, _uuid(raster)); url != "http://test.localhost/out/"+_uuid(raster) {
		t.Errorf("local endpoint %s", url)
	}
}

func TestCsvPointsPublishesADeliverableProximityRaster(t *testing.T) {
	test_tmpdir(t)

	reference := filepath.Join(tmpdir, "reference.geojson")
	os.WriteFile(reference, []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},
		"geometry":{"type":"Polygon","coordinates":[[[29,-2],[31,-2],[31,0],[29,0],[29,-2]]]}}]}`), 0644)

	points := filepath.Join(tmpdir, "points")
	os.WriteFile(points, []byte("lng,lat,name\n29.5,-1.5,a\n30.2,-0.4,b\n30.9,-1.9,c\n"), 0644)

	bucket := s3config{Name: "local", Provider: local_provider, Bucket: "http://test.localhost/out", Directory: filepath.Join(tmpdir, "bucket")}

	res, err := routine_csv_points(noopReporter, routine_params{
		S3:         bucket,
		Dataset:    points,
		Reference:  reference,
		Resolution: 1000,
		LngLat:     [2]string{"lng", "lat"},
		Fields:     []string{"name"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out struct{ Raster string }
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("%s: %v", res, err)
	}

	published := filepath.Join(bucket.Directory, _uuid(out.Raster))
	assert_deliverable_layout(t, published, "1", "2")

	ds, err := gdal.Open(published, gdal.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	if dt := ds.RasterBand(1).RasterDataType(); dt != gdal.Int16 {
		t.Errorf("proximity raster type %v, want Int16", dt)
	}
	if nodata, ok := ds.RasterBand(1).NoDataValue(); !ok || nodata != -1 {
		t.Errorf("proximity nodata %v/%v, want -1", nodata, ok)
	}
}

func copy_file(t *testing.T, from, to string) {
	t.Helper()

	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0644); err != nil {
		t.Fatal(err)
	}
}
