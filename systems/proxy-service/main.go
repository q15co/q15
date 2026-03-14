// Package main starts the q15 proxy-service binary.
package main

import (
	"fmt"
	"os"

	"github.com/q15co/q15/systems/proxy-service/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
