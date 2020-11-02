package main

import (
	"./gdal"
	"fmt"
)

func adm_id_raster(in filename, gid string) (filename, error) {
	//  -a Identifies an attribute field on the features to be used for a burn-in
	//     value. The value will be burned into all output bands.
	//
	opts := []string{
		"-tr", res, res,
		"-a", gid,
		"-of", "GTiff",
		"-ot", "Int16",
		"-a_nodata", "-1",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	source, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer source.Close()

	dest, err := gdal.Rasterize(out, source, opts)
	if err != nil {
		return "", err
	}
	dest.Close()

	println("admidraster output:", out)

	return out, err
}

func geometry_raster(in filename, lname string) (filename, error) {
	opts := []string{
		"-l", lname,
		"-burn", "1",
		"-ts", res, res, // get the bounds!
		"-a_nodata", "0",
		"-ot", "Byte",
		"-of", "GTiff",
		"-co", "COMPRESS=DEFLATE",
		"-co", "PREDICTOR=1",
		"-co", "ZLEVEL=9",
	}

	source, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	defer source.Close()

	dest, err := gdal.Rasterize(out, source, opts)
	if err != nil {
		panic(err)
	}
	defer dest.Close()

	println("rasterise output:", out)

	return out, err
}

func proximity_raster(in filename) (filename, error) {
	opts := []string{
		"DISTUNITS=PIXEL",
		fmt.Sprintf("MAXDIST=%d", 512),
		fmt.Sprintf("NODATA=%d", -1),
	}

	source, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
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

	ds := drv.CreateCopy(out, source, 0, opts2, gdal.DummyProgress, nil)
	err = source.
		RasterBand(1).
		ComputeProximity(ds.RasterBand(1), opts, gdal.DummyProgress, nil)

	ds.Close()

	println("proximity output:", out)

	return out, err
}
