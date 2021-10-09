package main

import (
	"errors"
	"fmt"
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

	release := capture()
	dest, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}
	defer dest.Close()

	return out, err
}

func vectors_reproject(in filename, epsg int) (filename, error) {
	out := _filename()

	opts := []string{
		"-t_srs", "EPSG:" + strconv.Itoa(epsg),
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	release := capture()
	dst, err := gdal.VectorTranslate(out+".geojson", []gdal.Dataset{src}, opts)

	result := release()
	if err != nil {
		return "", errors.New(result)
	}
	dst.Close()

	os.Rename(out+".geojson", out)

	return out, nil
}

func vectors_clip(in filename, container filename, w reporter) (filename, error) {
	w("VECTORS CLIP")

	out := _filename()

	src := gdal.OpenDataSource(in, 0).LayerByIndex(0)
	tar := gdal.OpenDataSource(container, 0).LayerByIndex(0)

	tt, _ := src.FeatureCount(true)
	w("	source feature count: %d", tt)

	ct, _ := tar.FeatureCount(true)
	w("	container feature count: %d", ct)
	if ct > 1 {
		return "", errors.New(fmt.Sprintf(
			"	The container file has %d features. It should have 1: the contour of the geography. \n"+
				"This is a configuration error on the geography.", ct))
	}

	drv := gdal.OGRDriverByName("GeoJSON")
	ds, _ := drv.Create(out, []string{})

	s := gdal.CreateSpatialReference("")
	s.FromEPSG(4326)

	res := ds.CreateLayer("Layer0", s, src.Type(), []string{})

	w("	clipping...")
	release := capture()
	err := src.Clip(tar, res, []string{})

	result := release()
	if err != nil {
		return "", errors.New(result)
	}

	ds.Destroy()

	l := gdal.OpenDataSource(out, 0).LayerByIndex(0)

	rt, _ := l.FeatureCount(true)
	w("	result feature count: %d", rt)

	return out, err
}
