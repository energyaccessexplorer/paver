package main

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// POST /deploy/<TICKET> ships the paver binary, POST /deploy/<TICKET>/departer
// the departer one; both land as <tickets-path>/<TICKET>/<service>.
var deploy_path_re = regexp.MustCompile(`^/deploy/([A-Z]+-[0-9]+)(/(paver|departer))?$`)

// Sanity cap, not a tight budget.
const max_binary_size = 200 << 20

// POST /deploy/<TICKET>[/<service>], binary as the body. Writes atomically
// (temp file + rename) and kills any already-running instance of that service
// for the ticket.
func deploy_handler(reg *registry, deployToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}

		if !valid_deploy_token(r, deployToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		m := deploy_path_re.FindStringSubmatch(r.URL.Path)
		if m == nil {
			http.Error(w, "expected /deploy/<TICKET>[/<service>]", http.StatusBadRequest)
			return
		}
		ticket, service := m[1], servicePaver
		if m[3] != "" {
			service = m[3]
		}

		dir := filepath.Join(reg.cfg.ticketsPath, ticket)
		if err := os.MkdirAll(dir, 0755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		final := filepath.Join(dir, service)
		tmp := final + ".upload"

		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		n, err := io.Copy(f, io.LimitReader(r.Body, max_binary_size+1))
		f.Close()
		if err != nil {
			os.Remove(tmp)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n > max_binary_size {
			os.Remove(tmp)
			http.Error(w, "binary too large", http.StatusRequestEntityTooLarge)
			return
		}
		if n == 0 {
			os.Remove(tmp)
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}

		if err := os.Rename(tmp, final); err != nil {
			os.Remove(tmp)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		logger.Printf("[DEPLOY] ticket=%s service=%s bytes=%d", ticket, service, n)

		reg.kill_if_running(ticket, service, "redeployed")

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "deployed %s/%s (%d bytes)\n", ticket, service, n)
	}
}

func valid_deploy_token(r *http.Request, token string) bool {
	if token == "" {
		return false // unconfigured means refuse, not allow
	}

	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return false
	}

	got := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
