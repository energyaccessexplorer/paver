package main

import (
	"fmt"
	"io"
	"net/http"
	"nhooyr.io/websocket"
	"time"
)

var (
	socket *websocket.Conn
)

func server_endpoints(mux *http.ServeMux) {
	mux.HandleFunc("/socket", _socket)
	mux.HandleFunc("/routines", _routines)

	mux.Handle("/", http.FileServer(http.Dir("public/")))
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
