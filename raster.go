package main

import (
	"fmt"
	"github.com/energyaccessexplorer/gdal"
)

func raster_ids(in filename, gid string) (filename, error) {
	out := _filename()

	//  -a Identifies an attribute field on the features to be used for a burn-in
	//     value. The value will be burned into all output bands.
	//
	opts := []string{
		"-a", gid,
		"-a_nodata", "-1",
		"-a_srs", "EPSG:3857",
		"-tr", "1000", "1000",
		"-of", "GTiff",
		"-ot", "Int16",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dest, err := gdal.Rasterize(out, src, opts)
	if err != nil {
		return "", err
	}
	dest.Close()

	return out, err
}

func raster_geometry(in filename, dst filename) (filename, error) {
	opts := []string{
		"-l", gdal.OpenDataSource(in, 0).LayerByIndex(0).Name(),
		"-burn", "1",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	defer src.Close()

	dest, err := gdal.OpenEx(dst, gdal.OFUpdate, nil, nil, nil)
	if err != nil {
		panic(err)
	}

	out, err := gdal.RasterizeOverwrite(dest, src, opts)
	if err != nil {
		panic(err)
	}
	defer out.Close()

	// TODO: dest.Close() segfaults... defer o no defer below, no comprende

	return dst, err
}

func raster_proximity(in filename) (filename, error) {
	out := _filename()

	opts := []string{
		"DISTUNITS=PIXEL",
		"VALUES=1",
		"NODATA=-1",
		"USE_INPUT_NODATA=YES",
		fmt.Sprintf("MAXDIST=%d", 512),
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
	}

	drv, err := gdal.GetDriverByName("GTiff")
	if err != nil {
		panic(err)
	}

	opts2 := []string{
		"COMPRESS=DEFLATE",
		"PREDICTOR=1",
		"ZLEVEL=9",
	}

	ds := drv.CreateCopy(out, src, 0, opts2, gdal.DummyProgress, nil)
	err = src.
		RasterBand(1).
		ComputeProximity(ds.RasterBand(1), opts, gdal.DummyProgress, nil)

	ds.Close()

	return out, err
}

func raster_zeros(in filename) (filename, error) {
	out := _filename()

	opts := []string{
		"-burn", "0",
		"-a_nodata", "-1",
		"-a_srs", "EPSG:3857",
		"-tr", "1000", "1000",
		"-of", "GTiff",
		"-ot", "Int16",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	defer src.Close()

	dest, err := gdal.Rasterize(out, src, opts)
	if err != nil {
		panic(err)
	}
	defer dest.Close()

	return out, err
}
