package main

import (
	"flag"
)

var (
	run_server bool
	run_cli    bool
)

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

	flag.StringVar(&idattr, "g", default_idattr, "blah blah")

	flag.Var(&selectattrs, "s", "Attributes to extract from the features")

	// SERVER flags
	//
	flag.StringVar(&dir, "dir", ".", "Directory where uploads should be finally stored.")
	flag.StringVar(&tmpdir, "tmpdir", ".", "Directory where uploads should be stored whilst uploading.")

	flag.StringVar(&jwtkey, "jwtkey", "", "Secret key to check JWT's.")
	flag.Var(&roles, "role", "Roles permitted in the JWT claims")

	flag.Parse()
}
