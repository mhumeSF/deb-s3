package main

import (
	"os"

	"github.com/deb-s3/deb-s3/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
