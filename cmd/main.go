package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"q15.co/sandbox/internal/app"
)

func main() {
	mode := flag.String("mode", "cli", "run mode: cli or bot")
	model := flag.String("model", "kimi-k2.5", "model name")
	flag.Parse()

	ctx := context.Background()

	var err error
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "bot":
		err = app.RunBot(ctx, *model)
	case "cli":
		err = app.RunCLI(ctx, *model)
	default:
		err = fmt.Errorf("invalid mode %q (expected cli or bot)", *mode)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
