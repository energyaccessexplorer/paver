package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/dgrijalva/jwt-go"
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
	jwtkey string
	roles  arrayFlag
	tmpdir string
	dir    string
)

type handler func(w http.ResponseWriter, r *http.Request)

type formdata map[string][]byte

func serve() {
	check_server_flags()

	fmt.Printf("Destination is '%s'\n", dir)
	fmt.Printf("Temporary directory is '%s'\n", tmpdir)

	fmt.Printf("JWT key is: %s\n", jwtkey)
	fmt.Printf("Role claim is: %s\n", roles)

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
	d, err := os.Open(dir)
	if err != nil {
		log.Fatal(errors.New("Specified directory does not exist!"))
	}
	d.Close()

	t, err := os.Open(tmpdir)
	if err != nil {
		log.Fatal(errors.New("Specified temporary directory does not exist!"))
	}
	t.Close()

	if jwtkey == "" {
		log.Fatal(errors.New("I won't start without a secret-key!"))
	}
}

func jwt_check(fn handler) handler {
	return func(w http.ResponseWriter, r *http.Request) {
		auth_header := r.Header.Get("Authorization")

		if auth_header == "" {
			http.Error(w, "No Authorization header.", http.StatusUnauthorized)
			return
		}

		tokenString := strings.TrimPrefix(auth_header, "Bearer ")

		token, err := jwt.Parse(
			tokenString,
			func(token *jwt.Token) (interface{}, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
				}

				return []byte(jwtkey), nil
			})

		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			for _, ro := range roles {
				if ro == claims["role"] {
					fn(w, r)
					return
				}
			}

			http.Error(w, "Who do think you are?", http.StatusUnauthorized)

		} else {
			fmt.Println("Naughty: ", claims["email"])

			w.WriteHeader(401)
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
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

func socketwrite(m string, r *http.Request) {
	if r == nil {
		fmt.Println(m)
		return
	}

	ctx := r.Context()
	socket.Write(ctx, websocket.MessageText, []byte(m))
}
