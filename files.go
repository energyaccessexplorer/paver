package main

import (
	"fmt"
	"github.com/satori/go.uuid"
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
