package main

import (
	"errors"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
	"math"
	"os/exec"
	"strconv"
)

type raster_config struct {
	Numbertype string `json:"numbertype"`
	Nodata     int    `json:"nodata"`
	Resample   string `json:"resample"`
}

func raster_ids(in filename, gid string, res int, w reporter) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	out := _filename()

	_res := strconv.Itoa(res)

	opts := []string{
		"-a", gid,
		"-a_srs", "EPSG:3857",
		"-a_nodata", "-1",
		"-tr", _res, _res,
		"-of", "GTiff",
		"-ot", "Int16",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	release := capture()
	dest, err := gdal.Rasterize(out, src, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}
	dest.Close()

	return out, err
}

func raster_geometry_ones(in filename, zs filename, w reporter) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}

	zeros, err := gdal.OpenEx(zs, gdal.OFUpdate, nil, nil, nil)
	if err != nil {
		return "", err
	}

	f := gdal.OpenDataSource(in, 0)
	defer f.Destroy()

	layer := f.LayerByIndex(0)

	opts := []string{
		"-l", layer.Name(),
		"-burn", "1",
	}

	_, ok := layer.FeatureCount(false)
	if !ok {
		return "", errors.New("Could not get feature count")
	}

	release := capture()
	err = gdal.RasterizeOverwrite(zeros, src, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}

	zeros.Close()
	src.Close()

	return zs, err
}

func raster_geometry_attr(in filename, zs filename, a string, w reporter) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}

	zeros, err := gdal.OpenEx(zs, gdal.OFUpdate, nil, nil, nil)
	if err != nil {
		return "", err
	}

	f := gdal.OpenDataSource(in, 0)
	defer f.Destroy()

	layer := f.LayerByIndex(0)

	opts := []string{
		"-l", layer.Name(),
		"-a", a,
	}

	_, ok := layer.FeatureCount(false)
	if !ok {
		return "", errors.New("Could not get feature count")
	}

	release := capture()
	err = gdal.RasterizeOverwrite(zeros, src, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}

	zeros.Close()
	src.Close()

	return zs, err
}

func raster_proximity(in filename, w reporter) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	drv, err := gdal.GetDriverByName("GTiff")
	if err != nil {
		return "", err
	}

	out := _filename()

	opts := []string{
		"-ot", "Int16",
		"-nodata", "-1",
		"DISTUNITS=PIXEL",
		"VALUES=1",
		"USE_INPUT_NODATA=YES",
		fmt.Sprintf("MAXDIST=%d", 1024),
	}

	ds := drv.CreateCopy(out, src, 0, []string{}, gdal.DummyProgress, nil)

	release := capture()
	err = src.
		RasterBand(1).
		ComputeProximity(ds.RasterBand(1), opts, gdal.DummyProgress, nil)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}
	ds.Close()

	return out, err
}

func raster_zeros(in filename, res int, w reporter) (filename, error) {
	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	out := _filename()

	_res := strconv.Itoa(res)

	opts := []string{
		"-burn", "0",
		"-a_nodata", "-1",
		"-a_srs", "EPSG:3857",
		"-tr", _res, _res,
		"-of", "GTiff",
		"-ot", "Int16",
	}

	release := capture()
	dest, err := gdal.Rasterize(out, src, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}
	defer dest.Close()

	return out, err
}

func raster_crop(in filename, base filename, ref filename, rc raster_config, w reporter) (filename, error) {
	w("RASTER CROP (multi-band)")

	r, err := gdal.OpenEx(base, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer r.Close()

	out := _filename()

	// One band to warp: a source with an alpha band would warp it into the
	// output too, and the tool reads band 1 as the display band.
	display_src := _filename() + ".tif"
	if err := gdal_translate([]string{"-b", "1", in, display_src}); err != nil {
		return "", err
	}
	defer trash(display_src)

	one, err := gdal.OpenEx(display_src, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer one.Close()

	x := r.RasterXSize()
	y := r.RasterYSize()

	gt := r.GeoTransform()
	extent := []string{
		strconv.FormatFloat(gt[0], 'f', -1, 64),
		strconv.FormatFloat(gt[3]+float64(y)*gt[5], 'f', -1, 64),
		strconv.FormatFloat(gt[0]+float64(x)*gt[1], 'f', -1, 64),
		strconv.FormatFloat(gt[3], 'f', -1, 64),
	}

	w(" grid: (%d,%d) from the outline raster, nodata: %d", x, y, rc.Nodata)

	// The output is a single, uniformly-typed Float32 raster with three bands:
	//   1 = display value (configured resampling, e.g. near)
	//   2 = area-weighted sum (conservative: subpixels of a cell add to its value)
	//   3 = area-weighted average
	// sum/average must be Float32 or their fractional contributions would be
	// rounded away (the EAE-500 Rainfed bug). Band 1 shares the type so the three
	// bands can live in one GeoTIFF.
	gtiff_opts := []string{
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	warp := func(method string) (filename, error) {
		band_out := _filename()

		opts := []string{
			"-of", "GTiff",
			"-t_srs", r.Projection(),
			"-te", extent[0], extent[1], extent[2], extent[3],
			"-ts", strconv.Itoa(x), strconv.Itoa(y),
			"-r", method,
			"-ot", "Float32",
			"-dstnodata", strconv.Itoa(rc.Nodata),
			"-wo", "NUM_THREADS=ALL_CPUS",
		}
		opts = append(opts, gtiff_opts...)

		release := capture()
		dest, err := gdal.Warp(band_out, nil, []gdal.Dataset{one}, opts)

		result := release()
		if err != nil {
			return "", fmt.Errorf("warp (%s): %s", method, result)
		}
		dest.Close()

		return band_out, nil
	}

	var bands []filename
	defer func() { trash(bands...) }()

	for i, method := range []string{rc.Resample, "sum", "average"} {
		band, err := warp(method)
		if err != nil {
			return "", fmt.Errorf("band %d (%s): %w", i+1, method, err)
		}
		bands = append(bands, band)
		w(" band %d (%s) done", i+1, method)
	}

	// Stack the three single-band warps into one 3-band GeoTIFF.
	vrt := _filename() + ".vrt"
	defer trash(vrt)

	buildvrt_args := append([]string{"-separate", vrt}, bands...)
	cmd := exec.Command("gdalbuildvrt", buildvrt_args...)
	if buf, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("gdalbuildvrt: %v: %s", err, buf)
	}

	translate_args := []string{"-of", "GTiff"}
	translate_args = append(translate_args, gtiff_opts...)
	translate_args = append(translate_args, vrt, out)
	cmd = exec.Command("gdal_translate", translate_args...)
	if buf, err := cmd.CombinedOutput(); err != nil {
		trash(out)
		return "", fmt.Errorf("gdal_translate: %v: %s", err, buf)
	}

	if err := mask_by_base(out, base, rc.Nodata); err != nil {
		trash(out)
		return "", err
	}

	return out, nil
}

// mask_by_base blanks every cell of out that base marks as empty. The outline
// raster, not the cutline, decides which cells belong to the geography: a
// cutline drops the cells a coarse source covers only partly, zeros included.
func mask_by_base(out filename, base filename, nodata int) error {
	b, err := gdal.Open(base, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer b.Close()

	bb := b.RasterBand(1)
	base_nodata, has_nodata := bb.NoDataValue()

	o, err := gdal.Open(out, gdal.Update)
	if err != nil {
		return err
	}
	defer o.Close()

	width, height := b.RasterXSize(), b.RasterYSize()

	const rows = 256

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)

		inside := make([]float64, width*n)
		if err := bb.IO(gdal.Read, 0, y, width, n, inside, width, n, 0, 0); err != nil {
			return err
		}

		for i := 1; i <= o.RasterCount(); i++ {
			band := o.RasterBand(i)

			values := make([]float64, width*n)
			if err := band.IO(gdal.Read, 0, y, width, n, values, width, n, 0, 0); err != nil {
				return err
			}

			changed := false
			for k := range values {
				empty := math.IsNaN(inside[k]) || (has_nodata && inside[k] == base_nodata)
				if empty && values[k] != float64(nodata) {
					values[k] = float64(nodata)
					changed = true
				}
			}

			if changed {
				if err := band.IO(gdal.Write, 0, y, width, n, values, width, n, 0, 0); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
