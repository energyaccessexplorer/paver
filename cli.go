package main

import (
	"fmt"
	"strings"
)

var (
	idattr string

	selectattrs arrayFlag

	command       string
	inputfile     string
	referencefile string
	targetfile    string
)

var default_epsg = 4326
var default_idattr = "OBJECTID"

// -ot For the output bands to be of the indicated data type. Defaults to
//     Float64
//

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
			fmt.Println("bounds:", bounds(inputfile).ToJSON())
		}

	case "info":
		{
			fmt.Println("info:", info(inputfile))
		}

	case "zeros":
		{
			out, _ := zeros_raster(inputfile)

			println("zeroes output:", out)
		}

	case "strip":
		{
			if len(selectattrs) == 0 {
				panic("No -s (select attributes) given.")
			}

			out, _ := strip(inputfile, selectattrs)

			println("strip output:", out)
		}

	case "rasterise":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			out, _ := geometry_raster(inputfile, targetfile)

			println("rasterise output:", out)
		}

	case "proximity":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			r, _ := geometry_raster(inputfile, targetfile)

			out, _ := proximity_raster(r)

			println("proximity output:", out)
		}

	case "idsraster":
		{
			if idattr == "" {
				println("No -g (idattr) given. Will use 'OBJECTID'")
				idattr = default_idattr
			}

			out, _ := ids_raster(inputfile, idattr)

			println("ids_raster output:", out)
		}

	case "clip":
		{
			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			out, _ := clip(inputfile, targetfile)

			println("clip output:", out)
		}

	case "csv":
		{
			if len(selectattrs) == 0 {
				panic("No -s (select attributes) given.")
			}

			out, _ := csv(inputfile, selectattrs)

			println("cvs output:", out)
		}

	case "admin_boundaries":
		{
			if idattr == "" {
				println("No -g (idattr) given. Will use 'OBJECTID'")
				idattr = default_idattr
			}

			admin_boundaries(inputfile, idattr)
		}

	case "vectors_routine":
		{
			if idattr == "" {
				println("No -g (idattr) given. Will use 'OBJECTID'")
				idattr = default_idattr
			}

			if referencefile == "" {
				panic("No -r (referencefile) given:")
			}

			vectors_routine(inputfile, referencefile, []string{idattr})
		}

	case "vectors_clipped_routine":
		{
			if idattr == "" {
				println("No -g (idattr) given. Will use 'OBJECTID'")
				idattr = default_idattr
			}

			if referencefile == "" {
				panic("No -r (referencefile) given:")
			}

			vectors_clipped_routine(nil, inputfile, referencefile, []string{idattr})
		}

	default:
		{
			println("No (valid) -c command given:", command)
		}
	}
}
