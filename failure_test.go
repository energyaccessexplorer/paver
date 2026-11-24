package main

import "testing"

func TestFailuresRecoverableOrNot(t *testing.T) {
	for msg, want := range map[string]bool{
		`Head "https://s3.amazonaws.com/x": read tcp 1.2.3.4:5->6.7.8.9:443: read: connection reset by peer`:         true,
		`gdal_translate: exit status 1: ERROR 1: Recv failure: Connection reset by peer`:                             true,
		`Error: dataset 'https://wri-public-data.s3.amazonaws.com/EnergyAccess/paver-outputs/85d47cf2' openEx error`: true,
		`context deadline exceeded (Client.Timeout or context cancellation while reading body)`:                      true,
		`ERROR 11: HTTP response code: 429`:                            true,
		`POST https://x/storage/v1/object/b/k 503 Service Unavailable`: true,
		`upload failed`: true,
		`HEAD https://wri-public-data.s3.amazonaws.com/EnergyAccess/ETH/airports/ETH_Airports.geojson: 404 Not Found`:        false,
		`HEAD https://energydata.info/dataset/small-hydropower: 403 Forbidden`:                                               false,
		"gdal_translate: exit status 1: ERROR 4: `/tmp/_gdal_http_3af1' not recognized as being in a supported file format.": false,
		`got a web page, not the file`:                           false,
		`Error: dataset '/tmp/paver/3d8d3779-ea5f' openEx error`: false,
		`no csv column holds a value the published raster has`:   false,
	} {
		if transient(msg) != want {
			t.Errorf("transient(%q) = %v", msg, !want)
		}
	}
}
