package main

import (
	"net/http"
	"strings"
)

type server_routine func(*http.Request) (bool, error)

var server_routines = map[string]server_routine{
	"clip-proximity":   server_clip_proximity,
}

func server_clip_proximity(r *http.Request) (bool, error) {
	f := formdata{
		"dataseturl":   nil,
		"referenceurl": nil,
		"attrs":        nil,
	}

	err := form_parse(&f, r)
	if err != nil {
		return false, err
	}

	inputfile, err := snatch(string(f["dataseturl"]))
	if err != nil {
		return false, err
	}

	referencefile, err := snatch(string(f["referenceurl"]))
	if err != nil {
		return false, err
	}

	ok, err := routine_clip_proximity(
		r,
		inputfile,
		referencefile,
		strings.Split(string(f["attrs"]), ","),
	)

	if !ok {
		return false, err
	}

	return true, nil
}
