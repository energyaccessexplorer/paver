package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// /EAE-464/paver/routines?... -> ticket="EAE-464", service="paver", rest="/routines?...".
var ticket_path_re = regexp.MustCompile(`^/([A-Z]+-[0-9]+)/(paver|departer)(/.*)$`)

// /EAE-464/departer/builds/<file> — finished departer logs and zips.
var departer_builds_re = regexp.MustCompile(`^/([A-Z]+-[0-9]+)/departer/builds/(.+)$`)

func proxy_handler(reg *registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Finished exports are plain files, served whether or not an instance
		// is currently running (the download link must outlive the instance).
		if m := departer_builds_re.FindStringSubmatch(r.URL.Path); m != nil {
			serve_departer_build(reg, w, r, m[1], m[2])
			return
		}

		m := ticket_path_re.FindStringSubmatch(r.URL.Path)
		if m == nil {
			http.Error(w, "expected /<TICKET>/{paver,departer}/...", 400)
			return
		}
		ticket, service, rest := m[1], m[2], m[3]

		inst, err := reg.get_or_launch(ticket, service)
		if err == errNotFound {
			http.Error(w, "no staging build found for "+ticket+"/"+service, 404)
			return
		} else if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}

		r.URL.Path = rest // strip "/TICKET/<service>", same as the shared locations do

		inst.active.Add(1)
		defer inst.active.Add(-1)

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

// Serves /<TICKET>/departer/builds/<name> out of that ticket's builds dir. Only
// bare filenames are accepted — everything the departer writes there is flat
// (<id>, <id>.log, energyaccessexplorer-<id>.zip).
func serve_departer_build(reg *registry, w http.ResponseWriter, r *http.Request, ticket, name string) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		http.Error(w, "bad build file name", http.StatusBadRequest)
		return
	}

	f, err := os.Open(filepath.Join(reg.cfg.departerBuilds, ticket, name))
	if err != nil {
		http.Error(w, "no such build file", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "no such build file", http.StatusNotFound)
		return
	}

	http.ServeContent(w, r, name, info.ModTime(), f)
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
