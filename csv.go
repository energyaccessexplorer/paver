package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/energyaccessexplorer/gdal"
	"io"
	"os"
	"strings"
	"time"
)

func csv(in filename, attrs []string) (filename, error) {
	src := gdal.OpenDataSource(in, 0)
	layer := src.LayerByIndex(0).Name()

	cols := strings.Join(attrs, ",")
	sql := fmt.Sprintf("SELECT %s FROM \"%s\"", cols, layer)

	fis := info_fields(in)

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
	defer src.ReleaseResultSet(l)

	fname := _filename()

	file, _ := os.Create(fname)
	defer file.Close()

	_, err := io.WriteString(file, strings.Join(payload, "\n"))
	if err != nil {
		return "", err
	}
	file.Sync()

	return fname, nil
}