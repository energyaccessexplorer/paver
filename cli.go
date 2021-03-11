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
	if inputfile == "" {
		panic("No -i (input file) given")
	}

	switch command {
	case "bounds":
		{
			fmt.Println("bounds:", info_bounds(inputfile).ToJSON())
		}

	case "info":
		{
			fmt.Println("info:", info(inputfile))
		}

	case "zeros":
		{
			out, _ := raster_zeros(inputfile)

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

	case "idsraster":
		{
			out, _ := raster_ids(inputfile, idfield)

			println("ids_raster output:", out)
		}

	case "clip":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			out, _ := vectors_clip(inputfile, targetfile)

			println("clip output:", out)
		}

	case "csv":
		{
			if len(selectfields) == 0 {
				panic("No -s (select fields) given.")
			}

			out, _ := csv(inputfile, selectfields)

			println("cvs output:", out)
		}

	case "admin_boundaries":
		{
			routine_admin_boundaries(nil, inputfile, idfield)
		}

	case "routine_clip_proximity":
		{
			if referencefile == "" {
				panic("No -r (referencefile) given:")
			}

			routine_clip_proximity(nil, inputfile, referencefile, []string{idfield})
		}

	default:
		{
			println("No (valid) -c command given:", command)
		}
	}
}
