package main

import (
	"context"
	"fmt"
	"os"

	"q15.co/sandbox/internal/app"
)

func main() {
	// if err := app.RunBot(context.Background()); err != nil {
	// 	log.Fatal(err)
	// }

	if err := app.RunCLI(context.Background(), "kimi-k2.5"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
