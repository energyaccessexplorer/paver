package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"os"
	"regexp"
	"syscall"
)

var (
	run_server bool
	run_cli    bool
)

var UUID_REGEXP = regexp.MustCompile("[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}")

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
	flag.StringVar(&pubkeyfile, "pubkey", "", "Public key file to check JWTs")

	flag.Parse()
}

func _filename() filename {
	return tmpdir + "/" + uuid.NewV4().String()
}

func _uuid(s string) string {
	return fmt.Sprintf("%s", UUID_REGEXP.Find([]byte(s)))
}

func trash(files ...filename) {
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			fmt.Println(err)
		}
	}
}

func capture() func() string {
	r, w, _ := os.Pipe()

	ostderr, _ := syscall.Dup(syscall.Stderr)
	syscall.Dup2(int(w.Fd()), syscall.Stderr)

	return func() string {
		w.Close()
		syscall.Close(syscall.Stderr)

		var b bytes.Buffer
		io.Copy(&b, r)
		syscall.Dup2(ostderr, syscall.Stderr)

		return b.String()
	}
}
