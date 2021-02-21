package main

import (
	"net/http"
	"strings"
)

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
