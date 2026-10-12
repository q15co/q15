// Package main provides the q15 management command.
package main

import (
	"fmt"
	"os"

	"github.com/q15co/q15/systems/agent/internal/q15cli"
)

// q15Release is the release identifier the binary was built with, injected at
// link time by the release workflow (see .goreleaser.yaml). A build made
// outside a release keeps the default, which `q15 version` reports verbatim.
var q15Release = "dev"

func main() {
	if err := q15cli.Run(q15Release, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
