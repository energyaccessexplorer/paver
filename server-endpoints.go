package main

import (
	"fmt"
	"io"
	"net/http"
	"nhooyr.io/websocket"
	"strings"
	"time"
)

var (
	socket *websocket.Conn
)

type server_routine func(*http.Request) (bool, error)

var server_routines = map[string]server_routine{
	"admin-boundaries": server_admin_boundaries,
	"clip-proximity":   server_clip_proximity,
}

func _routines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusOK)

	case "POST":
		q := r.URL.Query().Get("routine")
		if q == "" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.WriteString(w, "routine query parameter is not optional")
			return
		}

		if rtn := server_routines[q]; rtn == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.WriteString(w, "don't know what you mean by: "+q)
		} else {
			if ok, err := rtn(r); !ok {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, err.Error())
			}
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func _socket(w http.ResponseWriter, r *http.Request) {
	var err error

	socket, err = websocket.Accept(w, r, nil)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	defer socket.Close(websocket.StatusNormalClosure, "done!")

	count := 0
	for {
		time.Sleep(10 * time.Second)
		if count > 9 {
			break
		}

		count += 1
	}
}

func server_endpoints(mux *http.ServeMux) {
	mux.HandleFunc("/socket", jwt_check(_socket))
	mux.HandleFunc("/routines", jwt_check(_routines))

	mux.Handle("/", http.FileServer(http.Dir(public)))
}

func server_admin_boundaries(r *http.Request) (bool, error) {
	f := formdata{
		"dataseturl": nil,
		"field":      nil,
	}

	err := form_parse(&f, r)
	inputfile, err := snatch(string(f["dataseturl"]))
	if err != nil {
		return false, err
	}

	if ok, err := routine_admin_boundaries(
		r,
		inputfile,
		string(f["field"]),
	); !ok {
		return false, err
	}

	return true, nil
}

func server_clip_proximity(r *http.Request) (bool, error) {
	f := formdata{
		"dataseturl":   nil,
		"referenceurl": nil,
		"fields":       nil,
	}

	err := form_parse(&f, r)
	if err != nil {
		return false, err
	}

	inputfile, err := snatch(string(f["dataseturl"]))
	if err != nil {
		return false, err
	}

	referencefile, err := snatch(string(f["referenceurl"]))
	if err != nil {
		return false, err
	}

	if ok, err := routine_clip_proximity(
		r,
		inputfile,
		referencefile,
		strings.Split(string(f["fields"]), ","),
	); !ok {
		return false, err
	}

	return true, nil
}
