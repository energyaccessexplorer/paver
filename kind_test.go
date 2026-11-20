package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/energyaccessexplorer/gdal"
)

func TestFileKind(t *testing.T) {
	test_tmpdir(t)

	raster := _filename()
	write_raster(t, raster, 8, 8, gdal.Byte, []test_band{{values: make([]float64, 64)}})

	files := map[string]string{
		"geojson": `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},"geometry":{"type":"Point","coordinates":[1,2]}}]}`,
		"json":    `{"presets": [1, 2]}`,
		"csv":     "\xef\xbb\xbfOBJECTID,ADM2\r\n0,Ainabkoi\r\n1,Ainamoi\r\n",
		"text":    "a note\nwith no table in it\n",
		"pdf":     "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n",
		"zip":     "PK\x03\x04\x14\x00\x00\x00",
		"page":    "<!DOCTYPE html><html><body>Sign in</body></html>",
		"binary":  "\x00\x01\x02\x03\xff\xfe",
	}

	path := map[string]filename{"raster": raster}
	for name, content := range files {
		path[name] = filepath.Join(tmpdir, name)
		if err := os.WriteFile(path[name], []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct{ file, hint, ext, mime string }{
		{"raster", "", ".tif", "image/tiff"},
		{"geojson", "", ".geojson", "application/geo+json"},
		{"json", "", ".json", "application/json"},
		{"csv", "", ".csv", "text/plain; charset=utf-8"},
		{"text", "", ".txt", "text/plain; charset=utf-8"},
		{"pdf", "", ".pdf", "application/pdf"},
		{"zip", "", ".zip", "application/zip"},
		{"page", "", ".html", "text/html; charset=utf-8"},
		{"binary", "", "", "application/octet-stream"},
		{"zip", ".xlsx", ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"geojson", ".json", ".json", "application/json"},
		{"binary", ".gz", "", "application/octet-stream"},
	} {
		ext, mime := file_kind(path[c.file], c.hint)
		if ext != c.ext || mime != c.mime {
			t.Errorf("%s (hint %q): %q %q, want %q %q", c.file, c.hint, ext, mime, c.ext, c.mime)
		}
	}

	if got := key_type("EnergyAccess/website/logos/unido.svg", path["binary"]); got != "image/svg+xml" {
		t.Errorf("a key's extension decides: %q", got)
	}
	if got := key_type("uploads/3068cae1-35ab-46ff-b991-8d1b3ab3e290", path["csv"]); got != "text/plain; charset=utf-8" {
		t.Errorf("a bare key asks the file: %q", got)
	}
}
