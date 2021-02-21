package main

import (
	"net/http"
	"strings"
)

type server_routine func(*http.Request) (bool, error)

var server_routines = map[string]server_routine{
	"vectors_clipped": server_vectors_clipped,
}

func server_vectors_clipped(r *http.Request) (bool, error) {
	f := formdata{
		"inputfile":     nil,
		"targetfile":    nil,
		"referencefile": nil,
		"attrs":         nil,
	}

	err := form_parse(&f, r)
	if err != nil {
		return false, err
	}

	inputfile, _ := snatch(string(f["inputfile"]))
	targetfile, _ := snatch(string(f["targetfile"]))
	referencefile, _ := snatch(string(f["referencefile"]))

	return vectors_clipped_routine(
		inputfile,
		targetfile,
		referencefile,
		strings.Split(string(f["attrs"]), ","),
	)
}
