package main

import (
	"./gdal"
	"fmt"
	"strconv"
)

func get_bounds(in filename) gdal.Geometry {
	t := gdal.CreateSpatialReference("")
	t.FromEPSG(default_epsg)

	source := gdal.OpenDataSource(in, 0)

	layer := source.LayerByIndex(0)

	env, err := layer.Extent(true)
	if err != nil {
		panic(err)
	}

	// text := fmt.Sprintf(
	// 	"MULTIPOINT (%.32f %.32f, %.32f %.32f)",
	// 	env.MinX(), env.MaxX(), env.MinY(), env.MaxY(),
	// )

	text := fmt.Sprintf(
		`POLYGON ((%.32f %.32f), (%.32f %.32f), (%.32f %.32f), (%.32f %.32f), (%.32f %.32f))`,
		env.MaxX(), env.MinY(), env.MaxX(), env.MaxY(), env.MinX(), env.MaxY(), env.MinX(), env.MinY(),
		env.MaxX(), env.MinY())

	// TODO: this looks like we are going around uselessly...
	//
	layer.ResetReading()
	geom := layer.NextFeature().Geometry()

	v, ok := geom.SpatialReference().AttrValue("AUTHORITY", 1)
	if !ok {
		panic(ok)
	}

	s := gdal.CreateSpatialReference("")
	i, err := strconv.Atoi(v)
	if err != nil {
		panic(err)
	}

	s.FromEPSG(i)

	g, err := gdal.CreateFromWKT(text, s)
	if err != nil {
		panic(err)
	}

	return g
}
