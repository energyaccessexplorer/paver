package main

import (
	"bytes"
	"errors"
	"fmt"
	"git.263.nu/f/srv"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var (
	pubkeyfile string
	roles      arrayFlag
	tmpdir     string
	socket     string
)

type formdata map[string][]byte

type H map[string]srv.Handler

func serve() {
	check_server_flags()

	fmt.Printf("Temporary directory is '%s'\n", tmpdir)
	fmt.Printf("Public key is: %s\n", pubkeyfile)
	fmt.Printf("Roles claim is: %s\n", roles)

	srv.Run(
		socket,
		[]srv.Route{
			{"/check", nil, H{"GET": _check}},
			{"/socket", nil, H{"GET": _socket}},
			{"/routines", roles, H{"POST": _routines}},
		},
		pubkeyfile,
	)
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
}

func uri_test(url string) (int, bool) {
	if !strings.HasPrefix(url, "http") {
		return http.StatusBadRequest, false
	}

	resp, _ := http.Head(url)

	return resp.StatusCode, (resp.StatusCode == http.StatusOK)
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
