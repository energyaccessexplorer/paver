package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/energyaccessexplorer/gdal"
)

// What an object is named and served as comes from one table, so its
// extension and its type always agree and never depend on the host's mime
// tables. Text types are those the Storage CDN compresses: it leaves
// text/csv alone, so CSV goes out as text/plain.
var object_types = map[string]string{
	".tif":     "image/tiff",
	".tiff":    "image/tiff",
	".geojson": "application/geo+json",
	".json":    "application/json",
	".csv":     "text/plain; charset=utf-8",
	".txt":     "text/plain; charset=utf-8",
	".html":    "text/html; charset=utf-8",
	".htm":     "text/html; charset=utf-8",
	".pdf":     "application/pdf",
	".zip":     "application/zip",
	".png":     "image/png",
	".jpg":     "image/jpeg",
	".jpeg":    "image/jpeg",
	".jfif":    "image/jpeg",
	".gif":     "image/gif",
	".svg":     "image/svg+xml",
	".gpkg":    "application/geopackage+sqlite3",
	".nc":      "application/x-netcdf",
	".h5":      "application/x-hdf5",
	".kml":     "application/vnd.google-earth.kml+xml",
	".kmz":     "application/vnd.google-earth.kmz",
	".xlsx":    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":     "application/vnd.ms-excel",
	".docx":    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

const unknown_type = "application/octet-stream"

// GDAL's text drivers (CSV, XYZ) would claim plain tables, so only these
// are asked.
var gdal_kinds = map[string]string{
	"GTiff":   ".tif",
	"COG":     ".tif",
	"GeoJSON": ".geojson",
	"GPKG":    ".gpkg",
	"PNG":     ".png",
	"JPEG":    ".jpg",
	"GIF":     ".gif",
	"netCDF":  ".nc",
	"HDF5":    ".h5",
}

// file_kind names what f is: the hint, an extension the file came with, when
// the table knows it; otherwise what GDAL or the first bytes say. A file
// nothing recognises has no extension.
func file_kind(f filename, hint string) (ext string, mime string) {
	if t, ok := object_types[hint]; ok {
		return hint, t
	}

	if ext = gdal_kind(f); ext == "" {
		ext = sniff_kind(f)
	}

	if ext == "" {
		return "", unknown_type
	}

	return ext, object_types[ext]
}

func gdal_kind(f filename) string {
	drivers := make([]string, 0, len(gdal_kinds))
	for d := range gdal_kinds {
		drivers = append(drivers, d)
	}

	ds, err := gdal.OpenEx(f, gdal.OFReadOnly|gdal.OFRaster|gdal.OFVector, drivers, nil, nil)
	if err != nil {
		return ""
	}
	defer ds.Close()

	return gdal_kinds[ds.Driver().ShortName()]
}

func sniff_kind(f filename) string {
	file, err := os.Open(f)
	if err != nil {
		return ""
	}
	defer file.Close()

	head := make([]byte, 4096)
	n, _ := io.ReadFull(file, head)
	head = head[:n]

	switch t := http.DetectContentType(head); {
	case t == "application/pdf":
		return ".pdf"
	case t == "application/zip":
		return ".zip"
	case strings.HasPrefix(t, "text/html"):
		return ".html"
	case strings.HasPrefix(t, "text/plain"):
		if looks_json(head) {
			return ".json"
		}
		if looks_csv(head) {
			return ".csv"
		}
		return ".txt"
	}

	return ""
}

func looks_json(head []byte) bool {
	b := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")

	return len(b) > 0 && (b[0] == '{' || b[0] == '[')
}

// looks_csv wants the complete lines of the head to split into the same
// number of comma-separated fields, at least two of them.
func looks_csv(head []byte) bool {
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	if len(head) == 4096 {
		lines = lines[:len(lines)-1]
	}

	fields, rows := 0, 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}

		c := strings.Count(l, ",") + 1
		if fields == 0 {
			fields = c
		}
		if c != fields || c < 2 {
			return false
		}
		rows++
	}

	return rows >= 2
}

// key_type is the type an object is served with, from its key's extension.
func key_type(key string, f filename) string {
	if t, ok := object_types[strings.ToLower(path.Ext(key))]; ok {
		return t
	}

	_, t := file_kind(f, "")
	return t
}
