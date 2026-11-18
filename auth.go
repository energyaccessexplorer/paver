package main

// Request tokens are JWTs signed with the keys a JWKS (or a single public key
// file) names. The signing algorithm is whatever that key list holds: the
// legacy auth service signs RS256, Supabase Auth signs ES256 and rotates its
// keys. So a verifier is built per key and picked by the token's kid, rather
// than one verifier for one key and one algorithm. The claims are read here
// because the issuers disagree on where the user and the role live: the legacy
// auth service puts them in "id" and "role", Supabase Auth in "sub" and a
// "role" that names a Postgres role, with the application role, if any, in
// app_metadata (set through its admin API, never by the token's subject).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cristalhq/jwt/v5"
)

type claims struct {
	ID    string `json:"id"`
	Sub   string `json:"sub"`
	Role  string `json:"role"`
	Email string `json:"email"`

	AppMetadata struct {
		Role string `json:"role"`
	} `json:"app_metadata"`
}

// user is the token's user id, whatever its issuer calls it.
func (c claims) user() string {
	if c.Sub != "" {
		return c.Sub
	}

	return c.ID
}

// roles are the roles the token grants in EAE, most specific first.
func (c claims) roles() []string {
	roles := []string{}

	for _, r := range []string{c.AppMetadata.Role, c.Role} {
		if r != "" {
			roles = append(roles, r)
		}
	}

	return roles
}

func (c claims) permits(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}

	for _, a := range allowed {
		if a == "*" {
			return true
		}

		for _, r := range c.roles() {
			if r == a {
				return true
			}
		}
	}

	return false
}

// auth_key is one verification key. A single key has an empty kid and is used
// whatever the token names; a key set is picked by the token's kid.
type auth_key struct {
	kid      string
	verifier jwt.Verifier
}

// auth_keys holds every key a token may be signed with; none means auth is
// off. auth_source is the JWKS address to reload when a token's kid is
// unknown — Supabase rotates keys — and is empty for a fixed key set.
var (
	auth_keys    []auth_key
	auth_source  string
	auth_fetched time.Time
)

// jwks_refresh bounds how often an unknown kid can trigger a reload.
var jwks_refresh = 5 * time.Minute

// auth_setup reads the public keys tokens are verified against. keyfile is a
// single PEM key; jwks is a JWKS document, a path or an address to fetch.
// Neither means nothing is verified, which is what srv does with an empty
// -pubkey.
func auth_setup(keyfile, jwks string) error {
	auth_keys, auth_source, auth_fetched = nil, "", time.Time{}

	if keyfile != "" {
		content, err := os.ReadFile(keyfile)
		if err != nil {
			return err
		}

		key, err := auth_pem(content)
		if err != nil {
			return errors.New("auth_setup: " + keyfile + ": " + err.Error())
		}

		auth_keys = append(auth_keys, key)
	}

	if jwks != "" {
		keys, err := auth_jwks_load(jwks)
		if err != nil {
			return errors.New("auth_setup: " + jwks + ": " + err.Error())
		}

		auth_keys = append(auth_keys, keys...)

		if u, err := url.Parse(jwks); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			auth_source, auth_fetched = jwks, time.Now()
		}
	}

	return nil
}

// auth_pem reads one PEM public key, RSA or EC.
func auth_pem(content []byte) (auth_key, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return auth_key{}, errors.New("no PEM block")
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return auth_key{}, err
	}

	switch k := key.(type) {
	case *rsa.PublicKey:
		v, err := jwt.NewVerifierRS(jwt.RS256, k)
		return auth_key{"", v}, err
	case *ecdsa.PublicKey:
		v, err := jwt.NewVerifierES(es_alg(k.Curve), k)
		return auth_key{"", v}, err
	}

	return auth_key{}, errors.New("not an RSA or EC public key")
}

// es_alg is the signature algorithm an elliptic curve's keys sign with.
func es_alg(curve elliptic.Curve) jwt.Algorithm {
	switch curve.Params().BitSize {
	case 384:
		return jwt.ES384
	case 521:
		return jwt.ES512
	}

	return jwt.ES256
}

// jwk is one key of a JWKS document (RFC 7517), the fields paver verifies with.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// auth_jwks_load fetches or reads a JWKS document and builds its verifiers.
func auth_jwks_load(source string) ([]auth_key, error) {
	var content []byte
	var err error

	if u, perr := url.Parse(source); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		client := &http.Client{Timeout: 10 * time.Second}
		r, err := client.Get(source)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			return nil, errors.New("GET " + source + ": " + r.Status)
		}
		if content, err = io.ReadAll(io.LimitReader(r.Body, 1<<20)); err != nil {
			return nil, err
		}
	} else {
		content, err = os.ReadFile(source)
	}

	if err != nil {
		return nil, err
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}

	keys := []auth_key{}
	for _, k := range doc.Keys {
		v, err := k.verifier()
		if err != nil {
			return nil, err
		}

		keys = append(keys, auth_key{k.Kid, v})
	}

	return keys, nil
}

// verifier builds the verifier a JWK's key verifies with. The token's
// algorithm is not read here: the verifier enforces its own, so a token
// cannot name a weaker one to sidestep its key.
func (k jwk) verifier() (jwt.Verifier, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64big(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b64int(k.E)
		if err != nil {
			return nil, err
		}
		return jwt.NewVerifierRS(rs_alg(k.Alg), &rsa.PublicKey{N: n, E: e})

	case "EC":
		curve, err := es_curve(k.Crv)
		if err != nil {
			return nil, err
		}
		x, err := b64big(k.X)
		if err != nil {
			return nil, err
		}
		y, err := b64big(k.Y)
		if err != nil {
			return nil, err
		}
		return jwt.NewVerifierES(es_alg(curve), &ecdsa.PublicKey{Curve: curve, X: x, Y: y})
	}

	return nil, errors.New("unknown key type " + k.Kty)
}

func rs_alg(alg string) jwt.Algorithm {
	switch alg {
	case "RS384":
		return jwt.RS384
	case "RS512":
		return jwt.RS512
	}

	return jwt.RS256
}

func es_curve(crv string) (elliptic.Curve, error) {
	switch crv {
	case "P-256", "":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521", "P-512":
		return elliptic.P521(), nil
	}

	return nil, errors.New("unknown curve " + crv)
}

// b64big and b64int read the base64url numbers a JWK carries, with or
// without padding.
func b64big(s string) (*big.Int, error) {
	b, err := b64bytes(s)
	if err != nil {
		return nil, err
	}

	return new(big.Int).SetBytes(b), nil
}

func b64int(s string) (int, error) {
	b, err := b64bytes(s)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, c := range b {
		n = n<<8 | int(c)
	}

	return n, nil
}

func b64bytes(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}

	return base64.URLEncoding.DecodeString(s)
}

// auth_key_for picks the key a token's header names. The set is reloaded from
// the JWKS address when the kid is unknown and the cooldown allows — Supabase
// rotates to a kid the cache does not hold. A lone key verifies whatever the
// token names, as a single PEM key does.
func auth_key_for(h jwt.Header) (jwt.Verifier, error) {
	match := func() (jwt.Verifier, bool) {
		for _, k := range auth_keys {
			if k.kid == h.KeyID {
				return k.verifier, true
			}
		}
		return nil, false
	}

	if v, ok := match(); ok {
		return v, nil
	}

	if auth_source != "" && time.Since(auth_fetched) > jwks_refresh {
		if keys, err := auth_jwks_load(auth_source); err == nil {
			auth_keys, auth_fetched = keys, time.Now()

			if v, ok := match(); ok {
				return v, nil
			}
		}
	}

	if len(auth_keys) == 1 {
		return auth_keys[0].verifier, nil
	}

	return nil, errors.New("no key for kid " + h.KeyID)
}

// auth_check verifies the request's bearer token and returns its claims.
// Roles are ignored when none are expected.
func auth_check(r *http.Request, allowed []string) (claims, error) {
	var c claims

	// No keys configured: auth is off.
	if len(auth_keys) == 0 {
		return c, nil
	}

	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" {
		return c, errors.New("No Authorization header")
	}

	token, err := jwt.ParseNoVerify([]byte(raw))
	if err != nil {
		return claims{}, errors.New("Invalid token: " + err.Error())
	}

	verifier, err := auth_key_for(token.Header())
	if err != nil {
		return claims{}, errors.New("Invalid token: " + err.Error())
	}

	if err := verifier.Verify(token); err != nil {
		return claims{}, errors.New("Invalid token: " + err.Error())
	}

	if err := token.DecodeClaims(&c); err != nil {
		return claims{}, errors.New("Invalid token: " + err.Error())
	}

	if !c.permits(allowed) {
		return claims{}, errors.New("Not allowed")
	}

	return c, nil
}
