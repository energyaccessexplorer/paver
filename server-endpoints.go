package main

import (
	"io"
	"net/http"
)

func server_endpoints(mux *http.ServeMux) {
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
		q := r.URL.Query().Get("routine")
		if q == "" {
			io.WriteString(w, "routine query parameter is not optional")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}

		if rtn := server_routines[q]; rtn == nil {
			io.WriteString(w, "don't know what you mean by: "+q)
			w.WriteHeader(http.StatusMethodNotAllowed)
		} else {
			rtn(r)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
