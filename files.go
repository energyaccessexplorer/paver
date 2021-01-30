package main

import (
	"fmt"
	"os"
)

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
