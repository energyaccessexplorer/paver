package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"
)

// /EAE-464/paver/routines?... -> ticket="EAE-464", rest="/routines?...".
var ticket_path_re = regexp.MustCompile(`^/([A-Z]+-[0-9]+)/paver(/.*)$`)

func proxy_handler(reg *registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := ticket_path_re.FindStringSubmatch(r.URL.Path)
		if m == nil {
			http.Error(w, "expected /<TICKET>/paver/...", 400)
			return
		}
		ticket, rest := m[1], m[2]

		inst, err := reg.get_or_launch(ticket)
		if err == errNotFound {
			http.Error(w, "no staging build found for "+ticket, 404)
			return
		} else if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}

		r.URL.Path = rest // strip "/TICKET/paver", same as the shared /paver/ nginx location does

		if is_websocket_upgrade(r) {
			hijack_and_pipe(w, r, inst.socket)
			return
		}

		rp := &httputil.ReverseProxy{
			Transport: unix_transport(inst.socket),
			Director: func(req *http.Request) {
				req.URL.Scheme = "http"
				req.URL.Host = "paver"
			},
		}
		rp.ServeHTTP(w, r)
	}
}

func is_websocket_upgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func unix_transport(socket string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socket)
		},
	}
}

// httputil.ReverseProxy can't handle Upgrade: websocket, so hijack the
// connection and pipe raw bytes to the upstream socket instead.
func hijack_and_pipe(w http.ResponseWriter, r *http.Request, socket string) {
	upstream, err := net.Dial("unix", socket)
	if err != nil {
		http.Error(w, "upstream unavailable", 502)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket proxying unsupported", 500)
		return
	}

	client, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", 500)
		return
	}
	defer client.Close()

	if err := r.Write(upstream); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, bufrw); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
