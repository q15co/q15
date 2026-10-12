// Package main provides the q15 management command.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/q15co/q15/systems/agent/internal/cli"
	"github.com/q15co/q15/systems/agent/internal/q15cli"
)

// q15Release is the release identifier the binary was built with, injected at
// link time by the release workflow (see .goreleaser.yaml). A build made
// outside a release keeps the default, which `q15 version` reports verbatim.
var q15Release = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	env := cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Release: q15Release,
	}

	// main is the only place the process exits, and the status comes from the
	// error: 2 for a command line that is wrong in shape, 1 for a command that
	// ran and failed.
	if err := q15cli.Run(ctx, env, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
