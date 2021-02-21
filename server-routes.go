package main

import (
	"io"
	"net/http"
)

type server_routine func(*http.Request) (bool, error)

var routines = map[string]server_routine{
	"vectors_clipped": server_vectors_clipped,
}

func server_routes(mux *http.ServeMux) {
	mux.HandleFunc("/files", _files)
	mux.HandleFunc("/routines", _routines)

	mux.Handle("/", http.FileServer(http.Dir("public/")))
}

func _files(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		f := formdata{
			"file":     nil,
			"location": nil,
		}

		form_parse(&f, r)

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

func _routines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusOK)

	case "POST":
		routine := r.URL.Query().Get("routine")
		if routine == "" {
			io.WriteString(w, "routine query parameter is not optional")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}

		routines["routine"](r)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
