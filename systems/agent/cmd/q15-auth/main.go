// Package main provides the q15-auth command.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/q15co/q15/systems/agent/internal/authcli"
	"github.com/q15co/q15/systems/agent/internal/cli"
)

func main() {
	env := cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	if err := authcli.Run(context.Background(), env, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
