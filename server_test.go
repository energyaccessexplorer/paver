package main

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRoutineReplySaysWhatMadeTheOutputs(t *testing.T) {
	COMMIT_SHA = "0ffc1af"
	defer func() { COMMIT_SHA = "" }()

	crop := func(reporter, routine_params) (string, error) {
		return `{ "raster": "https://bucket/abc.tif" }`, nil
	}

	p := routine_params{
		Dataset:    "https://bucket/source.tif",
		Resolution: 1000,
		Config:     raster_config{Numbertype: "Float32", Nodata: -32767, Resample: "max"},
	}

	jsonstr, err := routine_reply("crop-raster", crop)(noop_reporter, p)
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Raster string `json:"raster"`
		Paver  struct {
			Routine string `json:"routine"`
			Version string `json:"version"`
			Params  struct {
				Resolution int           `json:"resolution"`
				Config     raster_config `json:"config"`
				Dataset    string        `json:"dataseturl"`
			} `json:"params"`
		} `json:"paver"`
	}
	if err := json.Unmarshal([]byte(jsonstr), &reply); err != nil {
		t.Fatal(err, jsonstr)
	}

	if reply.Raster != "https://bucket/abc.tif" {
		t.Fatalf("outputs lost: %s", jsonstr)
	}
	if reply.Paver.Routine != "crop-raster" || reply.Paver.Version != "0ffc1af" {
		t.Fatalf("paver %+v", reply.Paver)
	}
	if reply.Paver.Params.Resolution != 1000 || reply.Paver.Params.Config != p.Config {
		t.Fatalf("params %+v", reply.Paver.Params)
	}
	if reply.Paver.Params.Dataset != "" {
		t.Fatalf("the source endpoint is the dataset's, not a parameter: %s", jsonstr)
	}

	failing := func(reporter, routine_params) (string, error) { return "", errors.New("no") }
	if _, err := routine_reply("crop-raster", failing)(noop_reporter, p); err == nil {
		t.Fatal("a failed routine replied")
	}
}
