package main

import (
	"github.com/energyaccessexplorer/gdal"
	"os"
	"strings"
)

func strip(in filename, attrs []string) (filename, error) {
	out := _filename()

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
