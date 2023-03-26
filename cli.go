package main

import (
	"fmt"
	"strings"
)

var (
	idfield string

	selectfields arrayFlag

	command       string
	inputfile     string
	basefile      string
	referencefile string
	targetfile    string
)

type arrayFlag []string

func (i *arrayFlag) String() string {
	return strings.Join(*i, ",")
}

func (i *arrayFlag) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func cli() {
	p := func(s string, x ...any) {
		println(fmt.Sprintf(s, x...))
	}

	if inputfile == "" {
		panic("No -i (input file) given")
	}

	switch command {
	case "bounds":
		{
			fmt.Println("bounds:", info_bounds(inputfile).ToJSON())
		}

	case "vectors-info":
		{
			fmt.Println("info:", vectors_info(inputfile))
		}

	case "subgeographies":
		{
			fmt.Println(routine_subgeographies(p, inputfile, idfield))
		}

	case "zeros":
		{
			out, _ := raster_zeros(inputfile, 1000)

			println("zeroes output:", out)
		}

	case "strip":
		{
			if len(selectfields) == 0 {
				panic("No -s (select fields) given.")
			}

			out, _ := vectors_strip(inputfile, selectfields)

			println("strip output:", out)
		}

	case "rasterise":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			out, _ := raster_geometry(inputfile, targetfile)

			println("rasterise output:", out)
		}

	case "proximity":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			r, _ := raster_geometry(inputfile, targetfile)

			out, _ := raster_proximity(r)

			println("proximity output:", out)
		}

	case "ids-raster":
		{
			out, _ := raster_ids(inputfile, idfield, 1000)

			println("ids_raster output:", out)
		}

	case "clip":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			out, _ := vectors_clip(inputfile, targetfile, p)

			println("clip output:", out)
		}

	case "csv":
		{
			if len(selectfields) == 0 {
				panic("No -s (select fields) given.")
			}

			out, _ := csv(inputfile, selectfields)

			println("csv output:", out)
		}

	case "csv-points":
		{
			if len(selectfields) == 0 {
				println("No -s (select fields) given.")
			}

			out, _ := csv_points(inputfile, [2]string{"Longitude", "Latitude"}, selectfields)

			println("csv output:", out)
		}

	case "admin-boundaries":
		{
			routine_admin_boundaries(nil, inputfile, idfield, 1000)
		}

	case "routine-clip-proximity":
		{
			if referencefile == "" {
				panic("No -r (referencefile) given:")
			}

			routine_clip_proximity(nil, inputfile, referencefile, []string{idfield}, 1000)
		}

	case "routine-crop-raster":
		{
			if referencefile == "" {
				panic("No -r (referencefile) given:")
			}

			if basefile == "" {
				panic("No -r (referencefile) given:")
			}

			routine_crop_raster(nil, inputfile, basefile, referencefile, "{\"nodata\": -1, \"numbertype\": \"Int16\", \"resample\": \"average\"}", 1000)
		}

	case "s3put":
		{
			s3put(inputfile)
		}

	default:
		{
			println("No (valid) -c command given:", command)
		}
	}
}
