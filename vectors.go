package main

import (
	"errors"
	"github.com/energyaccessexplorer/gdal"
	"os"
	"strconv"
	"strings"
)

func vectors_strip(in filename, fields []string) (filename, error) {
	out := _filename()

	opts := []string{
		"-f", "GeoJSON",
		"-select", strings.Join(fields, ","),
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dest, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)
	if err != nil {
		return "", err
	}
	defer dest.Close()

	return out, err
}

func vectors_reproject(in filename) (filename, error) {
	out := _filename()

	opts := []string{
		"-t_srs", "EPSG:3857",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
		return "", err
	}
	defer src.Close()

	dst, err := gdal.VectorTranslate(out+".geojson", []gdal.Dataset{src}, opts)
	if err != nil {
		panic(err)
		return "", err
	}
	dst.Close()

	os.Rename(out+".geojson", out)

	return out, nil
}

func vectors_clip(in filename, container filename) (filename, error) {
	out := _filename()

	src := gdal.OpenDataSource(in, 0).LayerByIndex(0)
	tar := gdal.OpenDataSource(container, 0).LayerByIndex(0)

	ct, _ := tar.FeatureCount(true)
	if ct > 1 {
		return "", errors.New("Clipping only supports single-featured reference datasets. Got " + strconv.Itoa(ct))
	}

	drv := gdal.OGRDriverByName("GeoJSON")
	ds, _ := drv.Create(out, []string{})

	s := gdal.CreateSpatialReference("")
	s.FromEPSG(4326)

	result := ds.CreateLayer("Layer0", s, src.Type(), []string{})

	err := src.Clip(tar, result, []string{})
	if err != nil {
		return "", err
	}

	ds.Destroy()

	return out, err
}
