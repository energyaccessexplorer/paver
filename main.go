package main

import (
	"flag"
	"fmt"
	"github.com/satori/go.uuid"
	"math/rand"
	"strings"
	"time"
)

var (
	idattr  string

	selectattrs arrayFlag

	command    string
	inputfile  string
	targetfile string
)

var default_epsg = 4326
var default_idattr = "OBJECTID"

type filename = string

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

func parse_flags() {
	flag.StringVar(&command, "c", "", "Subcommand")

	flag.StringVar(&inputfile, "i", "", "File to be processed")
	flag.StringVar(&targetfile, "t", "", "Target file to use as reference for clipping/cropping")

	flag.StringVar(&idattr, "g", default_idattr, "blah blah")

	flag.Var(&selectattrs, "s", "Attributes to extract from the features")

	flag.Parse()
}

func rand_filename() filename {
	return "./outputs/" + uuid.Must(uuid.NewV4()).String()
}

func main() {
	parse_flags()

	rand.Seed(time.Now().UnixNano())

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
			out, _ := geometry_raster(inputfile)

			println("rasterise output:", out)
		}

	case "proximity":
		{
			r, _ := geometry_raster(inputfile)

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

			if targetfile == "" {
				panic("No -t (targetfile) given:")
			}

			vectors_routine(inputfile, targetfile, []string{idattr})
		}

	default:
		{
			println("No (valid) -c command given:", command)
		}
	}
}
