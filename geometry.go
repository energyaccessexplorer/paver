package main

import (
	"./gdal"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type dataset_info struct {
	Fields       []string   `json:"fields"`
	FeatureCount int        `json:"featurecount"`
	Bounds       [4]float64 `json:"bounds"`
}

func featurecount(in filename) int {
	src := gdal.OpenDataSource(in, 0).LayerByIndex(0)
	cs, _ := src.FeatureCount(true)

	return cs
}

func bounds(in filename) gdal.Geometry {
	t := gdal.CreateSpatialReference("")
	t.FromEPSG(default_epsg)

	src := gdal.OpenDataSource(in, 0)

	layer := src.LayerByIndex(0)

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

func info(in filename) string {
	e := bounds(in).Envelope()

	i := dataset_info{
		fields(in),
		featurecount(in),
		[4]float64{e.MinX(), e.MinY(), e.MaxX(), e.MaxY()},
	}

	j, err := json.Marshal(i)
	if err != nil {
		fmt.Println(err)
	}

	return string(j)
}

func csv(in filename, attrs []string) (filename, error) {
	src := gdal.OpenDataSource(in, 0)
	layer := src.LayerByIndex(0).Name()

	cols := strings.Join(attrs, ",")
	sql := fmt.Sprintf("SELECT %s FROM \"%s\"", cols, layer)

	fis := fields(in)

	for _, a := range attrs {
		for _, fi := range fis {
			if fi == a {
				goto found
			}
		}

		return "", errors.New(fmt.Sprintf("Could not find attr %s in %s\n", a, fis))

	found:
		continue
	}

	var g gdal.Geometry
	l := src.ExecuteSQL(sql, g, "")

	var f0 gdal.Feature
	f0 = l.Feature(0)

	indexes := make([]int, len(attrs))
	for j, str := range attrs {
		indexes[j] = f0.FieldIndex(str)
	}

	var payload []string

	payload = append(payload, strings.Join(attrs, ","))

	var f *gdal.Feature
	for {
		f = l.NextFeature()
		if f == nil {
			break
		}

		var line []string
		var s string

		for i := range indexes {
			if i == -1 {
				fmt.Println("next!")
			} else {
				switch f.FieldDefinition(i).Type() {
				case 0:
					s = fmt.Sprintf("%d", f.FieldAsInteger(i))

				case 2:
					s = fmt.Sprintf("%f", f.FieldAsFloat64(i))

				case 4:
					s = f.FieldAsString(i)

				// case 6, 7: are deprecated by GDAL

				case 8:
					s = base64.StdEncoding.EncodeToString(f.FieldAsBinary(i))

				case 10:
					{
						t, ok := f.FieldAsDateTime(i)
						if !ok {
							panic("csv: Failed to parse Date")
						}
						s = fmt.Sprintf("%d-%d-%d", t.Year(), t.Month(), t.Day())
					}

				case 11:
					{
						t, ok := f.FieldAsDateTime(i)
						if !ok {
							panic("csv: Failed to parse DateTime")
						}

						s = t.Format(time.RFC3339)
					}

				case 12:
					s = fmt.Sprintf("%d", f.FieldAsInteger64(i))

				default:
					panic("I don't know this type!")
				}
			}

			line = append(line, s)
		}

		payload = append(payload, strings.Join(line, ","))
	}

	name, _ := generate_file(strings.Join(payload, "\n"))

	defer src.ReleaseResultSet(l)

	return name, nil
}
