// Package main starts the q15 sandbox application.
package main

import (
	"fmt"
	"os"

	"q15.co/sandbox/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
