package main

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/cristalhq/jwt/v4"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"nhooyr.io/websocket"
	"os"
	"strconv"
	"strings"
)

var (
	pubkeyfile string
	pubkey     *rsa.PublicKey
	verifier   *jwt.RSAlg
	roles      arrayFlag
	tmpdir     string
)

type handler func(w http.ResponseWriter, r *http.Request)

type formdata map[string][]byte

func serve() {
	check_server_flags()

	fmt.Printf("Temporary directory is '%s'\n", tmpdir)

	fmt.Printf("Public key is: %s\n", pubkeyfile)
	fmt.Printf("Roles claim is: %s\n", roles)

	mux := http.NewServeMux()
	server_endpoints(mux)

	l, err := net.Listen("unix", "/tmp/paver-server.sock")
	if err != nil {
		panic(err)
	}

	os.Chmod("/tmp/paver-server.sock", 0777)

	fmt.Println("Listening on socket:", "/tmp/paver-server.sock")
	panic(http.Serve(l, mux))
}

func check_server_flags() {
	_, err := os.Stat(tmpdir)
	if os.IsNotExist(err) {
		log.Println(errors.New("Specified temporary directory does not exist. Creating..."))
		os.Mkdir(tmpdir, 0755)
	}

	t, err := os.Open(tmpdir)
	if err != nil {
		log.Fatal(errors.New("Specified temporary directory (still) does not exist!"))
	}
	t.Close()

	content, err := ioutil.ReadFile(pubkeyfile)
	if err != nil {
		log.Fatal(err.Error())
	}

	pubkey = parsekey(content)

	verifier, err = jwt.NewVerifierRS(jwt.RS256, pubkey)
	if err != nil {
		log.Fatal(errors.New("No verifier"))
	}
}

func parsekey(s []byte) *rsa.PublicKey {
	block, _ := pem.Decode(s)
	if block == nil {
		panic("Could not decode PEM bytes")
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(err)
	}

	return key.(*rsa.PublicKey)
}

func verifytoken(token *jwt.Token, pubkey *rsa.PublicKey) (ok bool, err error) {
	ok = false

	verifier, err := jwt.NewVerifierRS(jwt.RS256, pubkey)
	if err != nil {
		return ok, err
	}

	err = verifier.Verify(token)
	if err == nil {
		ok = true
	}

	return ok, err
}

func jwt_check(fn handler) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		auth_header := r.Header.Get("Authorization")

		if auth_header == "" {
			http.Error(w, "No Authorization header.", http.StatusUnauthorized)
			return
		}

		ts := strings.TrimPrefix(auth_header, "Bearer ")

		t, err := jwt.Parse([]byte(ts), verifier)

		ok, err := verifytoken(t, pubkey)
		if !ok {
			http.Error(w, "Shoo!", http.StatusForbidden)
		}

		var c userclaims

		err = jwt.ParseClaims([]byte(ts), verifier, &c)

		if err == nil {
			for _, ro := range roles {
				if ro == c.Role {
					fn(w, r)
					return
				}
			}
		}

		http.Error(w, "Could not get role", http.StatusUnauthorized)

		return
	}
}

func uri_test(url string) (status int, success bool) {
	if !strings.HasPrefix(url, "http") {
		success = false
		status = http.StatusBadRequest
		return
	}

	resp, err := http.Head(url)
	if err != nil {
		log.Fatal(err)
	}

	success = (resp.StatusCode == http.StatusOK)
	status = resp.StatusCode
	return
}

func snatch(location string) (fname string, err error) {
	fname = _filename()

	for _, x := range []string{"geojson", "shp", "tiff"} {
		if strings.HasSuffix(location, "."+x) {
			fname += "." + x
			break
		}
	}

	if status, ok := uri_test(location); !ok {
		err = errors.New("Couldn not fetch '" + location + "' - Error: " + strconv.Itoa(status))
		return
	}

	resp, e := http.Get(location)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()

	file, err := os.Create(fname)
	if err != nil {
		return "", err
	}
	defer file.Close()

	body, err := ioutil.ReadAll(resp.Body)

	if _, err := io.Copy(file, bytes.NewReader(body)); err != nil {
		return "", err
	}

	return fname, nil
}

func form_parse(form *formdata, r *http.Request) (err error) {
	t := r.Header.Get("Content-Type")

	if strings.HasPrefix(t, "multipart/form-data") {
		reader, e := r.MultipartReader()

		if e != nil {
			err = e
			return
		}

		r.ParseMultipartForm(0) // do not use any memory - it all goes to disk.

		for {
			part, e := reader.NextPart()

			if e == io.EOF {
				break
			}

			for k, _ := range *form {
				if part.FormName() == k {
					buf := new(bytes.Buffer)
					buf.ReadFrom(part)
					(*form)[k] = buf.Bytes()
				}
			}
		}
	}

	if strings.HasPrefix(t, "application/x-www-form-urlencoded") {
		r.ParseForm()

		for k, _ := range *form {
			(*form)[k] = []byte(r.FormValue(k))
		}
	}

	return err
}

func socket_write(s *websocket.Conn, m string, r *http.Request) {
	if r == nil {
		fmt.Println(m)
		return
	}

	ctx := r.Context()
	s.Write(ctx, websocket.MessageText, []byte(m))
}
