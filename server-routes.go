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

		if len(f["file"]) > 0 {
			if result, err := catch(f["file"]); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			} else {
				io.WriteString(w, result)
			}
		} else if len(f["location"]) > 0 {
			if result, err := snatch(string(f["location"])); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			} else {
				io.WriteString(w, result)
			}
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
