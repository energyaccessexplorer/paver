package main

import (
	"./gdal"
	"strings"
)

func strip(in filename, attrs []string) (filename, error) {
	opts := []string{
		"-f", "GeoJSON",
		"-simplify", res,
		"-select", strings.Join(attrs, ","),
	}

	source, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer source.Close()

	dest, err := gdal.VectorTranslate(out, []gdal.Dataset{source}, opts)
	if err != nil {
		return "", err
	}
	defer dest.Close()

	return out, err
}
