package main

import (
	"context"
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

func socket_destroy(id string, s *websocket.Conn, m string) {
	s.Close(websocket.StatusNormalClosure, m)
	delete(socket_table, id)
}

func socket_create(id string, w http.ResponseWriter, r *http.Request) {
	s, err := websocket.Accept(w, r, nil)
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	socket_table[id] = s

	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Minute)
	defer cancel()

	select {
	case <-ctx.Done():
		socket_destroy(id, s, fmt.Sprintf("timed out - %v", ctx.Err()))
	}
}
