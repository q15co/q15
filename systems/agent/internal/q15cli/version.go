package q15cli

import (
	"fmt"
	"io"

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
func runVersion(release string, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("version takes no arguments, got %q", args[0])
	}

	fmt.Fprintf(stdout, "release: %s\n", release)
	fmt.Fprintf(stdout, "config_schema: %d\n", config.SchemaVersion)
	fmt.Fprintf(stdout, "state_layout: %d\n", StateLayoutVersion)
	return nil
}
