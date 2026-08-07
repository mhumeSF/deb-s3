package main

import (
	"os"

	"github.com/mhumesf/deb-s3-go/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
