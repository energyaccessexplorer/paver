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
