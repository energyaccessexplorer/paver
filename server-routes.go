package main

import (
	"io"
	"net/http"
)

func server_routes(mux *http.ServeMux) {
	mux.HandleFunc("/files", files)

	mux.Handle("/", http.FileServer(http.Dir("public/")))
}

func files(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		f := formdata{
			"file":     nil,
			"location": nil,
		}

		form_parse(&f, r, w)

		if len(f["location"]) > 0 {
			catch(f, w)
		} else if len(f["file"]) > 0 {
			snatch(f, w)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
