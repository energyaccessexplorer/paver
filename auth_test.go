package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	test_key_once sync.Once
	test_key      *rsa.PrivateKey
)

// auth_test_key is the key tokens are signed with; auth_setup is pointed at its
// private-less PEM file.
func auth_test_key(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	test_key_once.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}

		test_key = key
	})

	der, err := x509.MarshalPKIXPublicKey(&test_key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	auth_tmpdir(t)
	file := filepath.Join(tmpdir, "public.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}

	if err := auth_setup(file, ""); err != nil {
		t.Fatal(err)
	}

	return test_key
}

// auth_tmpdir points the package's tmpdir at a test-local directory.
func auth_tmpdir(t *testing.T) {
	t.Helper()
	tmpdir = t.TempDir()
}

func b64(v any) string {
	var b []byte

	switch v := v.(type) {
	case []byte:
		b = v
	default:
		b, _ = json.Marshal(v)
	}

	return base64.RawURLEncoding.EncodeToString(b)
}

// token signs claims as the legacy auth service does (RS256, no kid) when kid
// is empty, and as Supabase Auth does when it is set.
func token(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}

	body := b64(header) + "." + b64(claims)

	digest := sha256.Sum256([]byte(body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	return body + "." + b64(signature)
}

func auth_request(authorization string) *http.Request {
	r := httptest.NewRequest("POST", "/routines?routine=simplify", nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}

	return r
}

func TestAuthAcceptsSupabaseToken(t *testing.T) {
	key := auth_test_key(t)

	supabase := token(t, key, "eae-dev", map[string]any{
		"sub":        "0b6b4a6f-2e26-4afa-a38e-c641d2ee9957",
		"role":       "authenticated",
		"email":      "someone@example.org",
		"aud":        "authenticated",
		"iat":        1700000000,
		"exp":        1700003600,
		"session_id": "c4b5a1d0-0000-4000-8000-000000000000",
	})

	c, err := auth_check(auth_request("Bearer "+supabase), nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := c.user(), "0b6b4a6f-2e26-4afa-a38e-c641d2ee9957"; got != want {
		t.Errorf("user %s, want %s", got, want)
	}
}

func TestAuthAcceptsLegacyToken(t *testing.T) {
	key := auth_test_key(t)

	legacy := token(t, key, "", map[string]any{
		"id":  "5c841971-573a-4d6c-b382-f85d41a6684f",
		"iat": 1700000000,
		"exp": 1700086400,
	})

	c, err := auth_check(auth_request("Bearer "+legacy), nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := c.user(), "5c841971-573a-4d6c-b382-f85d41a6684f"; got != want {
		t.Errorf("user %s, want %s", got, want)
	}
}

func TestAuthRejectsOtherKeys(t *testing.T) {
	auth_test_key(t)

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	foreign := token(t, other, "eae-dev", map[string]any{"sub": "someone"})

	if _, err := auth_check(auth_request("Bearer "+foreign), nil); err == nil {
		t.Error("a token signed by another key was accepted")
	}

	if _, err := auth_check(auth_request(""), nil); err == nil {
		t.Error("a request without a token was accepted")
	}

	if _, err := auth_check(auth_request("Bearer nonsense"), nil); err == nil {
		t.Error("a malformed token was accepted")
	}
}

func TestAuthRoles(t *testing.T) {
	key := auth_test_key(t)

	legacy := token(t, key, "", map[string]any{"id": "u", "role": "director"})
	if _, err := auth_check(auth_request("Bearer "+legacy), []string{"admin", "director"}); err != nil {
		t.Errorf("legacy role rejected: %s", err)
	}
	if _, err := auth_check(auth_request("Bearer "+legacy), []string{"admin"}); err == nil {
		t.Error("legacy role accepted where it is not allowed")
	}

	// Supabase Auth's own "role" is a Postgres role; the application role the
	// admin API pins on a user lives in app_metadata.
	supabase := token(t, key, "eae-dev", map[string]any{
		"sub":          "u",
		"role":         "authenticated",
		"app_metadata": map[string]any{"role": "director"},
	})

	if _, err := auth_check(auth_request("Bearer "+supabase), []string{"director"}); err != nil {
		t.Errorf("app_metadata role rejected: %s", err)
	}

	if _, err := auth_check(auth_request("Bearer "+supabase), []string{"admin"}); err == nil {
		t.Error("app_metadata role accepted where it is not allowed")
	}

	// user_metadata is writable by the token's own subject.
	selfgranted := token(t, key, "eae-dev", map[string]any{
		"sub":           "u",
		"role":          "authenticated",
		"user_metadata": map[string]any{"role": "director"},
	})

	if _, err := auth_check(auth_request("Bearer "+selfgranted), []string{"director"}); err == nil {
		t.Error("user_metadata role accepted")
	}

	if _, err := auth_check(auth_request("Bearer "+supabase), []string{"*"}); err != nil {
		t.Errorf("wildcard rejected a valid token: %s", err)
	}
}

// token_es signs claims the way Supabase Auth does: ES256, under a kid.
func token_es(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header := map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid}
	body := b64(header) + "." + b64(claims)

	digest := sha256.Sum256([]byte(body))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	// JOSE's ES256 signature is r || s, each the curve's width.
	width := key.Curve.Params().BitSize / 8
	sig := make([]byte, 2*width)
	r.FillBytes(sig[:width])
	s.FillBytes(sig[width:])

	return body + "." + b64(sig)
}

func jwk_rsa(t *testing.T, kid string, key *rsa.PublicKey) map[string]any {
	t.Helper()

	return map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256",
		"n": b64(key.N.Bytes()),
		"e": b64(big.NewInt(int64(key.E)).Bytes()),
	}
}

func jwk_ec(t *testing.T, kid string, key *ecdsa.PublicKey) map[string]any {
	t.Helper()

	return map[string]any{
		"kty": "EC", "kid": kid, "alg": "ES256", "crv": "P-256",
		"x": b64(key.X.Bytes()),
		"y": b64(key.Y.Bytes()),
	}
}

func jwks_file(t *testing.T, keys ...map[string]any) string {
	t.Helper()

	b, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(file, b, 0644); err != nil {
		t.Fatal(err)
	}

	return file
}

// A key set covers both issuers: the legacy service's RS256 and Supabase
// Auth's ES256, each under its own kid, and refuses everything else.
func TestAuthJWKSCoversBothIssuers(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := auth_setup("", jwks_file(t,
		jwk_rsa(t, "legacy", &rsaKey.PublicKey),
		jwk_ec(t, "eae-dev", &ecKey.PublicKey),
	)); err != nil {
		t.Fatal(err)
	}

	if _, err := auth_check(auth_request("Bearer "+token(t, rsaKey, "legacy", map[string]any{"id": "u", "role": "director"})), []string{"director"}); err != nil {
		t.Errorf("legacy RS256 token rejected: %s", err)
	}

	if _, err := auth_check(auth_request("Bearer "+token_es(t, ecKey, "eae-dev", map[string]any{"sub": "u", "app_metadata": map[string]any{"role": "director"}})), []string{"director"}); err != nil {
		t.Errorf("Supabase ES256 token rejected: %s", err)
	}

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth_check(auth_request("Bearer "+token_es(t, other, "eae-dev", map[string]any{"sub": "u"})), nil); err == nil {
		t.Error("a token signed by another key was accepted")
	}

	// An RS256 token naming the EC key's kid must not ride that key's name.
	if _, err := auth_check(auth_request("Bearer "+token(t, rsaKey, "eae-dev", map[string]any{"sub": "u"})), nil); err == nil {
		t.Error("an RS256 token was accepted under the ES256 key's kid")
	}
}

// A JWKS at an address is fetched, and reloaded when Supabase rotates to a
// kid the cached set does not hold.
func TestAuthJWKSRotatesFromAddress(t *testing.T) {
	jwks_refresh = 0
	t.Cleanup(func() { jwks_refresh = 5 * time.Minute })

	old, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	doc := []byte(`{"keys":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(doc)
	}))
	t.Cleanup(srv.Close)

	publish := func(kid string, key *ecdsa.PrivateKey) {
		b, err := json.Marshal(map[string]any{"keys": []map[string]any{jwk_ec(t, kid, &key.PublicKey)}})
		if err != nil {
			t.Fatal(err)
		}
		doc = b
	}

	publish("kid-a", old)
	if err := auth_setup("", srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := auth_check(auth_request("Bearer "+token_es(t, old, "kid-a", map[string]any{"sub": "u"})), nil); err != nil {
		t.Errorf("fetched JWKS rejected: %s", err)
	}

	// Supabase rotates to a new key under a new kid.
	next, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publish("kid-b", next)

	if _, err := auth_check(auth_request("Bearer "+token_es(t, next, "kid-b", map[string]any{"sub": "u"})), nil); err != nil {
		t.Errorf("rotated key rejected: %s", err)
	}
}

// TestAuthTokensThroughHandler sends both token forms at the routine handler:
// the legacy service's RS256 token and Supabase Auth's ES256 token. Both clear
// the handler's auth gate (the request then fails on its own validation); a
// missing or foreign-signed token is refused.
func TestAuthTokensThroughHandler(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Staging's Supabase key id, so the ES256 example has its real shape.
	const supabaseKid = "e9f3cefe-7e7d-4e48-a0c1-f74e503d506c"

	if err := auth_setup("", jwks_file(t,
		jwk_rsa(t, "eae-legacy", &rsaKey.PublicKey),
		jwk_ec(t, supabaseKid, &ecKey.PublicKey),
	)); err != nil {
		t.Fatal(err)
	}

	legacy := token(t, rsaKey, "eae-legacy", map[string]any{
		"id": "5c841971-573a-4d6c-b382-f85d41a6684f", "role": "director",
	})
	supabase := token_es(t, ecKey, supabaseKid, map[string]any{
		"sub":          "0b6b4a6f-2e26-4afa-a38e-c641d2ee9957",
		"role":         "authenticated",
		"app_metadata": map[string]any{"role": "director"},
	})

	t.Logf("legacy   RS256 token: %s…", legacy[:48])
	t.Logf("supabase ES256 token: %s…", supabase[:48])

	cases := []struct {
		name, bearer string
		authed       bool
	}{
		{"legacy RS256 (kid eae-legacy)", "Bearer " + legacy, true},
		{"supabase ES256 (kid " + supabaseKid[:8] + "…)", "Bearer " + supabase, true},
		{"no token", "", false},
		{"foreign ES256 key", "Bearer " + token_es(t, foreign, supabaseKid, map[string]any{"sub": "u"}), false},
		{"RS256 under the ES256 kid", "Bearer " + token(t, rsaKey, supabaseKid, map[string]any{"sub": "u"}), false},
	}

	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/routines", nil)
		if tc.bearer != "" {
			r.Header.Set("Authorization", tc.bearer)
		}
		w := httptest.NewRecorder()
		_routines(w, r)

		if got := w.Code != http.StatusForbidden; got != tc.authed {
			t.Errorf("%s: handler returned HTTP %d (%s), want authed=%v", tc.name, w.Code, strings.TrimSpace(w.Body.String()), tc.authed)
		}

		t.Logf("%-32s HTTP %d  %s", tc.name, w.Code, strings.TrimSpace(w.Body.String()))
	}
}
