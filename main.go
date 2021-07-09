package main

import (
	"flag"
	"fmt"
	"github.com/satori/go.uuid"
	"os"
)

var (
	run_server bool
	run_cli    bool
)

type filename = string

func main() {
	parse_flags()

	if run_server {
		serve()
	} else if run_cli {
		cli()
	} else {
		panic("What am I supposed to do? I am just a program.")
	}
}

func parse_flags() {
	flag.BoolVar(&run_server, "server", false, "Should I server")
	flag.BoolVar(&run_cli, "cli", false, "Should I CLI")

	// CLI flags
	//
	flag.StringVar(&command, "c", "", "Subcommand")

	flag.StringVar(&inputfile, "i", "", "File to be processed")
	flag.StringVar(&targetfile, "t", "", "Target file to use as reference for clipping/cropping")
	flag.StringVar(&referencefile, "r", "", "File to be used as reference")

	flag.StringVar(&idfield, "g", "OBJECTID", "blah blah")

	flag.Var(&selectfields, "s", "Fields to extract from the features")

	// SERVER flags
	//
	flag.Var(&roles, "role", "Roles permitted in the JWT claims")

	flag.Parse()
}

func _filename() filename {
	return tmpdir + "/" + uuid.NewV4().String()
}

func trash(files ...filename) {
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			fmt.Println(err)
		}
	}
}
