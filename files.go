package main

import (
	"fmt"
	"github.com/satori/go.uuid"
	"io"
	"os"
)

type filename = string

func rand_filename() filename {
	return "./outputs/" + uuid.Must(uuid.NewV4()).String()
}

func cleanup_files(files ...filename) {
	for _, f := range files {
		err := os.Remove(f)

		if err == nil {
			fmt.Println("cleanup", f)
		} else {
			fmt.Println(err)
		}
	}
}

func generate_file(payload string) (filename, error) {
	file, err := os.Create(rand_filename())
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.WriteString(file, payload)
	if err != nil {
		return "", err
	}

	return file.Name(), file.Sync()
}
