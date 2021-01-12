package main

import (
	"./gdal"
)

func fields(in filename) []string {
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

func clip(in filename, bin filename) gdal.Layer {
	source := gdal.OpenDataSource(in, 0).LayerByIndex(0)
	target := gdal.OpenDataSource(bin, 0).LayerByIndex(0)

	ct, _ := target.FeatureCount(true)
	if ct > 1 {
		println("Counted", ct, "features in", in)
		panic("Clipping only supports single-featured datasets")
	}

	drv := gdal.OGRDriverByName("GeoJSON")
	ds, ok := drv.Create(out, []string{})
	if !ok {
		panic(ok)
	}

	s := gdal.CreateSpatialReference("")
	s.FromEPSG(default_epsg)

	result := ds.CreateLayer(layername, s, source.Type(), []string{})

	err := source.Intersection(target, result, []string{})
	if err != nil {
		panic(err.Error())
	}

	ds.Destroy()

	println("clip output:", out)

	return target
}
