package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/onatm/boxface/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("boxfacectl", version.Version)
		return
	}
	flag.Usage()
	os.Exit(2)
}
