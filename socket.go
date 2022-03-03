package main

import (
	"fmt"
	"net/http"
	"nhooyr.io/websocket"
	"time"
)

var socket_table = map[string]*websocket.Conn{}

func socket_write(s *websocket.Conn, m string, r *http.Request) {
	if r == nil {
		fmt.Println(m)
		return
	}

	s.Write(r.Context(), websocket.MessageText, []byte(m))
}

func socket_destroy(id string, s *websocket.Conn) {
	s.Close(websocket.StatusNormalClosure, "done!")
	delete(socket_table, id)
}

func socket_create(id string, w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	defer socket_destroy(id, socket)

	socket_table[id] = socket

	count := 0
	for {
		time.Sleep(10 * time.Second)
		if count > 9 {
			break
		}

		count += 1
	}
}
