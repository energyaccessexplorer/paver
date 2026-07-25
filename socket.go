package main

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"net/http"
	"sync"
	"time"
)

var SOCKET_ACCEPT_PATTERN string

var (
	socket_table    = map[string]*websocket.Conn{}
	socket_table_mu sync.RWMutex
)

func socket_get(id string) *websocket.Conn {
	socket_table_mu.RLock()
	defer socket_table_mu.RUnlock()
	return socket_table[id]
}

// socket_write reports m to the socket currently registered under id. The
// table is consulted at write time (never captured in a closure) so a
// reconnected client keeps receiving progress and a long-finished HTTP
// request cannot cancel the write.
func socket_write(id string, m string) string {
	s := socket_get(id)

	if s == nil {
		logger.Println(m)
		return m
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
		logger.Println("socket_write:", err.Error())
	}

	return m
}

// socket_destroy closes s and drops it from the table — but only if the
// table still points at s, so a reconnected (newer) socket under the same
// id is never closed by an old owner's cleanup.
func socket_destroy(id string, s *websocket.Conn, m string) {
	socket_table_mu.Lock()
	if socket_table[id] == s {
		delete(socket_table, id)
	}
	socket_table_mu.Unlock()

	if s == nil {
		logger.Println(m)
		return
	}

	s.Close(websocket.StatusNormalClosure, m)
}

func socket_create(id string, w http.ResponseWriter, r *http.Request) {
	s, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{SOCKET_ACCEPT_PATTERN},
	})

	if err != nil {
		logger.Println(err.Error())
		return
	}

	socket_table_mu.Lock()
	socket_table[id] = s
	socket_table_mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	defer cancel()

	// Long GDAL phases produce no progress messages for many minutes;
	// without traffic, browsers and proxies drop the silent connection.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := s.Ping(pctx)
				pcancel()

				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	<-ctx.Done()

	socket_destroy(id, s, fmt.Sprintf("timed out - %v", ctx.Err()))
}
