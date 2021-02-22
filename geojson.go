package main

import (
	"github.com/energyaccessexplorer/gdal"
	"strings"
)

func strip(in filename, attrs []string) (filename, error) {
	out := rand_filename()

	opts := []string{
		"-f", "GeoJSON",
		"-select", strings.Join(attrs, ","),
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

func reproject(in filename) (filename, error) {
	out := rand_filename() + ".geojson"

	opts := []string{
		"-t_srs", "EPSG:3857",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		panic(err)
		return "", err
	}
	defer src.Close()

	dst, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)
	if err != nil {
		panic(err)
		return "", err
	}
	defer dst.Close()

	return out, nil
}
