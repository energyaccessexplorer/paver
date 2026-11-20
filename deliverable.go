package main

// Every GeoTIFF leaves paver in one encoding, whatever wrote it. Readers fetch
// whole files and whole bands, so the layout serves decoding: one plane per
// band, so a reader decodes only the bands it asks for, in 64-row strips
// (taller decode no faster; tiles decode slower and only help reading spatial
// windows). DEFLATE with or without a predictor, whichever is smaller:
// differencing pays on smooth fields and costs on categorical ones. Encoding
// never changes a value; deliver checks.

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
	"io"
	"math"
	"os"
	"os/exec"
)

// deliverable_version names the encoding; bump it when deliverable_options
// changes so `paver repave` redoes what it already did.
const deliverable_version = "1"

const deliverable_strip_rows = 64

func deliverable_options(predictor string) []string {
	return []string{
		"-of", "GTiff",
		"-co", "COMPRESS=DEFLATE",
		"-co", "ZLEVEL=9",
		"-co", "PREDICTOR=" + predictor,
		"-co", "INTERLEAVE=BAND",
		"-co", fmt.Sprintf("BLOCKYSIZE=%d", deliverable_strip_rows),
		"-co", "NUM_THREADS=ALL_CPUS",
	}
}

func is_geotiff(f filename) bool {
	file, err := os.Open(f)
	if err != nil {
		return false
	}
	defer file.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(file, magic); err != nil {
		return false
	}

	for _, m := range [][]byte{
		{'I', 'I', 42, 0}, {'M', 'M', 0, 42}, // classic
		{'I', 'I', 43, 0}, {'M', 'M', 0, 43}, // BigTIFF
	} {
		if bytes.Equal(magic, m) {
			return true
		}
	}

	return false
}

// differencing_predictor is the TIFF predictor that suits a sample type:
// 3 (floating point) for floats, 2 (horizontal) for integers.
func differencing_predictor(in filename) (string, error) {
	ds, err := gdal.Open(in, gdal.ReadOnly)
	if err != nil {
		return "", err
	}
	defer ds.Close()

	if ds.RasterCount() < 1 {
		return "", errors.New("raster has no bands: " + in)
	}

	switch ds.RasterBand(1).RasterDataType() {
	case gdal.Float32, gdal.Float64:
		return "3", nil
	default:
		return "2", nil
	}
}

func gdal_translate(args []string) error {
	cmd := exec.Command("gdal_translate", append([]string{"-q"}, args...)...)
	if buf, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gdal_translate: %v: %s", err, buf)
	}

	return nil
}

func file_size(f filename) int64 {
	st, err := os.Stat(f)
	if err != nil {
		return -1
	}

	return st.Size()
}

// raster_deliverable writes in's deliverable encoding to a new file, checks it
// holds exactly in's raster, and returns it with the predictor it chose.
func raster_deliverable(in filename) (filename, string, error) {
	differencing, err := differencing_predictor(in)
	if err != nil {
		return "", "", err
	}

	var best filename
	var chosen string

	for _, p := range []string{"1", differencing} {
		out := _filename() + ".tif"

		if err := gdal_translate(append(deliverable_options(p), in, out)); err != nil {
			trash(out)
			if best != "" {
				trash(best)
			}
			return "", "", err
		}

		if best == "" || file_size(out) < file_size(best) {
			if best != "" {
				trash(best)
			}
			best, chosen = out, p
		} else {
			trash(out)
		}
	}

	if err := rasters_identical(in, best); err != nil {
		trash(best)
		return "", "", fmt.Errorf("deliverable differs from its source: %w", err)
	}

	return best, chosen, nil
}

// raster_deliver re-encodes f in place, so its name and URL stay the same.
func raster_deliver(f filename, w reporter) error {
	out, p, err := raster_deliverable(f)
	if err != nil {
		return err
	}

	w(" deliverable: %d -> %d bytes (predictor %s)", file_size(f), file_size(out), p)

	return os.Rename(out, f)
}

// rasters_identical compares everything a reader can observe: size, bands,
// sample types, georeferencing, nodata, colour interpretation and every
// sample's bits.
func rasters_identical(a, b filename) error {
	da, err := gdal.Open(a, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer da.Close()

	db, err := gdal.Open(b, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer db.Close()

	if da.RasterXSize() != db.RasterXSize() || da.RasterYSize() != db.RasterYSize() {
		return fmt.Errorf("size %dx%d vs %dx%d", da.RasterXSize(), da.RasterYSize(), db.RasterXSize(), db.RasterYSize())
	}

	if da.RasterCount() != db.RasterCount() {
		return fmt.Errorf("band count %d vs %d", da.RasterCount(), db.RasterCount())
	}

	if da.GeoTransform() != db.GeoTransform() {
		return fmt.Errorf("geotransform %v vs %v", da.GeoTransform(), db.GeoTransform())
	}

	if da.Projection() != db.Projection() {
		return errors.New("projection differs")
	}

	for i := 1; i <= da.RasterCount(); i++ {
		ba, bb := da.RasterBand(i), db.RasterBand(i)

		if ba.RasterDataType() != bb.RasterDataType() {
			return fmt.Errorf("band %d: type %v vs %v", i, ba.RasterDataType(), bb.RasterDataType())
		}

		na, oka := ba.NoDataValue()
		nb, okb := bb.NoDataValue()
		if oka != okb || (oka && math.Float64bits(na) != math.Float64bits(nb)) {
			return fmt.Errorf("band %d: nodata %v/%v vs %v/%v", i, na, oka, nb, okb)
		}

		if ba.ColorInterp() != bb.ColorInterp() {
			return fmt.Errorf("band %d: colour interpretation differs", i)
		}

		if err := band_samples_identical(ba, bb, da.RasterXSize(), da.RasterYSize()); err != nil {
			return fmt.Errorf("band %d: %w", i, err)
		}
	}

	return nil
}

// band_samples_identical reads both bands in their own type, rows at a time,
// and compares the bits, so NaN payloads and signed zeros count.
func band_samples_identical(a, b gdal.RasterBand, width, height int) error {
	const rows = 256

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)

		sa, err := band_rows(a, y, n, width)
		if err != nil {
			return err
		}

		sb, err := band_rows(b, y, n, width)
		if err != nil {
			return err
		}

		if !bytes.Equal(sa, sb) {
			return fmt.Errorf("samples differ in rows %d..%d", y, y+n-1)
		}
	}

	return nil
}

func band_rows(band gdal.RasterBand, y, n, width int) ([]byte, error) {
	count := width * n

	var buf any
	var raw func() []byte

	switch band.RasterDataType() {
	case gdal.Byte:
		s := make([]uint8, count)
		buf, raw = s, func() []byte { return s }
	case gdal.Int16:
		s := make([]int16, count)
		buf, raw = s, func() []byte { return int16_bytes(s) }
	case gdal.UInt16:
		s := make([]uint16, count)
		buf, raw = s, func() []byte { return uint16_bytes(s) }
	case gdal.Int32:
		s := make([]int32, count)
		buf, raw = s, func() []byte { return int32_bytes(s) }
	case gdal.UInt32:
		s := make([]uint32, count)
		buf, raw = s, func() []byte { return uint32_bytes(s) }
	case gdal.Float32:
		s := make([]float32, count)
		buf, raw = s, func() []byte { return float32_bytes(s) }
	case gdal.Float64:
		s := make([]float64, count)
		buf, raw = s, func() []byte { return float64_bytes(s) }
	default:
		return nil, fmt.Errorf("unsupported sample type %v", band.RasterDataType())
	}

	if err := band.IO(gdal.Read, 0, y, width, n, buf, width, n, 0, 0); err != nil {
		return nil, err
	}

	return raw(), nil
}

func int16_bytes(s []int16) []byte {
	out := make([]byte, 0, 2*len(s))
	for _, v := range s {
		out = append(out, byte(v), byte(uint16(v)>>8))
	}
	return out
}

func uint16_bytes(s []uint16) []byte {
	out := make([]byte, 0, 2*len(s))
	for _, v := range s {
		out = append(out, byte(v), byte(v>>8))
	}
	return out
}

func int32_bytes(s []int32) []byte {
	u := make([]uint32, len(s))
	for i, v := range s {
		u[i] = uint32(v)
	}
	return uint32_bytes(u)
}

func uint32_bytes(s []uint32) []byte {
	out := make([]byte, 0, 4*len(s))
	for _, v := range s {
		out = append(out, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	return out
}

func float32_bytes(s []float32) []byte {
	u := make([]uint32, len(s))
	for i, v := range s {
		u[i] = math.Float32bits(v)
	}
	return uint32_bytes(u)
}

func float64_bytes(s []float64) []byte {
	out := make([]byte, 0, 8*len(s))
	for _, v := range s {
		u := math.Float64bits(v)
		for k := 0; k < 8; k++ {
			out = append(out, byte(u>>(8*k)))
		}
	}
	return out
}
