package main

/*
#ifndef GO_GDAL_H_
#define GO_GDAL_H_

#include <gdal.h>
#include <gdal_alg.h>
#include <gdal_utils.h>
#include <gdalwarper.h>
#include <cpl_conv.h>
#include <ogr_srs_api.h>

// transform GDALProgressFunc to go func
GDALProgressFunc goGDALProgressFuncProxyB();

#endif // GO_GDAL_H_
#include "gdal_version.h"
*/
import "C"

import (
	"archive/zip"
	"errors"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
	"os/exec"
	"strconv"
	"strings"
)

type intstringdict map[int64]string

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
		"-f", "GeoJSON",
		"-t_srs", "EPSG:" + strconv.Itoa(epsg),
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	release := capture()
	dst, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)
	defer dst.Close()

	result := release()
	if err != nil {
		return "", errors.New(result)
	}

	return out, nil
}

func vectors_clip(in filename, container filename, w reporter) (filename, error) {
	w("VECTORS CLIP")

	out := _filename()

	f := gdal.OpenDataSource(in, 0)
	g := gdal.OpenDataSource(container, 0)
	defer f.Destroy()
	defer g.Destroy()

	src := f.LayerByIndex(0)
	tar := g.LayerByIndex(0)

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

	h := gdal.OpenDataSource(out, 0)
	defer h.Destroy()

	l := h.LayerByIndex(0)

	rt, _ := l.FeatureCount(true)
	w("	result feature count: %d", rt)

	return out, err
}

func vectors_simplify(in filename, s float32) (filename, error) {
	out := _filename()

	opts := []string{
		"-f", "GeoJSON",
		"-simplify", fmt.Sprintf("%f", s),
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer src.Close()

	release := capture()
	dst, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)
	defer dst.Close()

	result := release()
	if err != nil {
		return "", errors.New(result)
	}

	return out, nil
}

func vectors_features_split(in filename, id string, w reporter) (intstringdict, error) {
	w("VECTORS FEATURES")

	f := gdal.OpenDataSource(in, 0)
	src := f.LayerByIndex(0)
	defer f.Destroy()

	drv := gdal.OGRDriverByName("GeoJSON")
	s := gdal.CreateSpatialReference("")
	s.FromEPSG(4326)

	results := make(intstringdict)

	for i := 0; i < vectors_feature_count(in); i++ {
		f := src.Feature(int64(i))
		x := f.FieldIndex(id)
		y := f.FieldAsInteger64(x)

		if x < 0 {
			w("YEAH, NAH...")
			return nil, errors.New("-1")
		}

		out := _filename()
		ds, _ := drv.Create(out, []string{})

		layer := ds.CreateLayer("Layer0", s, src.Type(), []string{})
		layer.Create(f)

		ds.Destroy()

		results[y] = out

		w("%d: %s", y, out)
	}

	return results, nil
}

func vectors_feature_count(in filename) int {
	f := gdal.OpenDataSource(in, 0)
	defer f.Destroy()

	src := f.LayerByIndex(0)
	cs, _ := src.FeatureCount(true)

	return cs
}

func vectors_info(in filename) dataset_info {
	b := info_bounds(in)
	if b.Type() == gdal.GT_None {
		return dataset_info{}
	}

	e := b.Envelope()

	return dataset_info{
		vectors_fields(in),
		vectors_feature_count(in),
		bounds{e.MinX(), e.MinY(), e.MaxX(), e.MaxY()},
	}
}

func vectors_fields(in filename) []string {
	f := gdal.OpenDataSource(in, 0)
	defer f.Destroy()

	fdef := f.
		LayerByIndex(0).
		Definition()

	c := fdef.FieldCount()
	a := make([]string, c)

	for i := 0; i < c; i++ {
		a[i] = fdef.FieldDefinition(i).Name()
	}

	return a
}

func _maybe_zip(in filename) string {
	C.CPLSetConfigOption(C.CString("SHAPE_RESTORE_SHX"), C.CString("NO"))

	fmt.Println("HERE 0")

	r, err := zip.OpenReader(in)
	if err == nil {
		in = "/vsizip/{" + in + "}"
	} else {
		return in
	}
	r.Close()

	fmt.Println("HERE 1", in)

	out := _filename()

	opts := []string{
		"-f", "GeoJSON",
	}

	src, err := gdal.OpenEx(in, gdal.OFReadOnly, nil, nil, nil)
	if err != nil {
		fmt.Println(err)
	}
	defer src.Close()

	fmt.Println("HERE 2")

	dest, err := gdal.VectorTranslate(out, []gdal.Dataset{src}, opts)
	if err != nil {
		fmt.Println(err)
	}
	defer dest.Close()

	fmt.Println("HERE 3")
	fmt.Println("HERE 4")

	return out
}

func maybe_zip(in filename) string {
	r, err := zip.OpenReader(in)
	if err == nil {
		in = "/vsizip/{" + in + "}"
	} else {
		return in
	}
	r.Close()

	out := _filename()

	cmd := exec.Command("ogr2ogr", "-f", "GeoJSON", out, in)
	stdout, err := cmd.Output()

	if err != nil {
		fmt.Println("cmd::::::", stdout)
	}

	fmt.Println(cmd.String())
	fmt.Println(out)

	return out
}
