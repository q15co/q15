// Package main provides the q15-auth command.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/q15co/q15/systems/agent/internal/authcli"
)

func main() {
	if err := authcli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
