package cli

import (
	"context"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/config"
)

// StateLayoutVersion is the version of the on-disk state layout the CLI
// writes. The stack's state directory is meant to carry it in a layout-version
// file from the first release, so a later CLI recognises an older layout
// instead of inferring it from what it finds there. Nothing writes that file
// yet; the constant is reported by `version` so a bug report carries it.
const StateLayoutVersion = 1

// runVersion reports the three identifiers. They are separate axes on purpose:
// the release moves with every build, the configuration schema moves when a
// written configuration changes shape, and the state layout version moves when
// the state directory does. The order is part of the contract, because a bug
// report quotes these three lines.
//
// An argument is refused rather than ignored: a mistyped invocation should fail
// where the mistake is, not print something plausible.
func runVersion(_ context.Context, env command.Env, args []string) error {
	if len(args) > 0 {
		return command.Usagef("version takes no arguments, got %q", args[0])
	}

	fmt.Fprintf(env.Stdout, "release: %s\n", env.Release)
	fmt.Fprintf(env.Stdout, "config_schema: %d\n", config.SchemaVersion)
	fmt.Fprintf(env.Stdout, "state_layout: %d\n", StateLayoutVersion)
	return nil
}
