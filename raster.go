package main

import (
	"errors"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
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
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
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

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	out := _filename()

	f := gdal.OpenDataSource(ref, 0)
	defer f.Destroy()

	layer := f.LayerByIndex(0).Name()
	w(" cropping to first layer: %s", layer)

	x := r.RasterXSize()
	y := r.RasterYSize()

	w(" raster size: (%d,%d)", x, y)
	w(" nodata: %d", rc.Nodata)

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
			"-t_srs", "EPSG:3857",
			"-ts", strconv.Itoa(x), strconv.Itoa(y),
			"-r", method,
			"-ot", "Float32",
			"-dstnodata", strconv.Itoa(rc.Nodata),
			"-cutline", ref,
			"-crop_to_cutline",
			"-cl", layer,
			"-wo", "NUM_THREADS=ALL_CPUS",
		}
		opts = append(opts, gtiff_opts...)

		release := capture()
		dest, err := gdal.Warp(band_out, nil, []gdal.Dataset{src}, opts)

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

	return out, nil
}
