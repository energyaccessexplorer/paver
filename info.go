package main

import (
	"encoding/json"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
	"log"
	"strconv"
)

type bounds struct {
	Left   float64
	Bottom float64
	Right  float64
	Top    float64
}

type dataset_info struct {
	Fields       []string `json:"fields"`
	FeatureCount int      `json:"featurecount"`
	Bounds       bounds   `json:"bounds"`
}

func info_featurecount(in filename) int {
	src := gdal.OpenDataSource(in, 0).LayerByIndex(0)
	cs, _ := src.FeatureCount(true)

	return cs
}

func info_bounds(in filename) gdal.Geometry {
	none := gdal.Create(gdal.GT_None)

	t := gdal.CreateSpatialReference("")
	t.FromEPSG(4326)

	src := gdal.OpenDataSource(in, 0)

	layer := src.LayerByIndex(0)

	env, err := layer.Extent(true)
	if err != nil {
		log.Println("Failed to get layer extent")
		return none
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
		log.Println("Failed setting AUTHORITY to spatial reference")
		return none
	}

	s := gdal.CreateSpatialReference("")
	i, err := strconv.Atoi(v)
	if err != nil {
		log.Println(err.Error())
		return none
	}

	s.FromEPSG(i)

	g, err := gdal.CreateFromWKT(text, s)
	if err != nil {
		log.Println(err.Error())
		return none
	}

	return g
}

func info_fields(in filename) []string {
	fdef := gdal.
		OpenDataSource(in, 0).
		LayerByIndex(0).
		Definition()

	c := fdef.FieldCount()
	a := make([]string, c)

	for i := 0; i < c; i++ {
		a[i] = fdef.FieldDefinition(i).Name()
	}

	return a
}

func info(in filename) string {
	b := info_bounds(in)
	if b.Type() == gdal.GT_None {
		return "Info bounds returned a garbage geometry"
	}

	e := b.Envelope()

	i := dataset_info{
		info_fields(in),
		info_featurecount(in),
		bounds{e.MinX(), e.MinY(), e.MaxX(), e.MaxY()},
	}

	j, err := json.Marshal(i)
	if err != nil {
		fmt.Println(err)
	}

	return string(j)
}
