package cli

import (
	"context"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// runDown stops the stack. It is up's mirror image, and it does not render or
// reload: stopping what is running is not a moment to change what is declared,
// and a daemon-reload here would be a change the operator did not ask for.
//
// Stopping every unit in one call lets systemd unwind them in dependency order,
// so the containers go before the network they joined. Running it twice is safe.
func runDown(ctx context.Context, env command.Env, args []string) error {
	flags := stackFlags{}
	fs := newStackFlagSet("down", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("down takes no arguments, got %q", fs.Arg(0))
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}
	units := profile.UnitNames()

	fmt.Fprintf(env.Stderr, "stopping %d units\n", len(units))
	if err := switchUnits(ctx, processRunner{env: env}, "stop", units); err != nil {
		return fmt.Errorf("stop the stack: %w", err)
	}
	return nil
}
