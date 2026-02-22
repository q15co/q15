// Package main starts the q15 sandbox application.
package main

import (
	"fmt"
	"os"

	"github.com/q15co/q15/systems/agent/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
