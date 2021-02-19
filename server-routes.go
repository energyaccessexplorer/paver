package main

import (
	"io"
	"net/http"
)

func server_routes(mux *http.ServeMux) {
	mux.HandleFunc("/", home)
	mux.HandleFunc("/files", files)
}

func home(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusOK)

	case "GET":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "Hej hej!")

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func files(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("Allow", "POST,GET")
		w.WriteHeader(http.StatusOK)

	case "GET":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, file_form_template)

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
