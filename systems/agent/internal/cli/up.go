package cli

import (
	"context"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/stack"
)

// runUp starts the stack: it asks the user's systemd to regenerate its units and
// then starts them, which is the two steps Quadlet needs in that order.
//
// Rendering is deliberately not here. The units on disk are the ones a setup
// wrote, and a command whose job is to start a stack should not also change what
// the stack says. Running it twice is safe because daemon-reload and
// `systemctl start` are both idempotent.
func runUp(ctx context.Context, env command.Env, args []string) error {
	flags := stackFlags{}
	fs := newStackFlagSet("up", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("up takes no arguments, got %q", fs.Arg(0))
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}
	units := profile.ServiceNames()

	runner := processRunner{env: env}
	fmt.Fprintf(env.Stderr, "reloading the user's systemd\n")
	if err := stack.Reload(ctx, runner); err != nil {
		return err
	}

	fmt.Fprintf(env.Stderr, "starting %d units\n", len(units))
	if err := switchUnits(ctx, runner, "start", units); err != nil {
		return fmt.Errorf("start the stack: %w", err)
	}
	return nil
}

// switchUnits starts or stops every unit in one systemctl call, so systemd
// orders the transitions itself from the units' own dependencies rather than the
// CLI guessing at an order it would have to keep in step with the renderer.
func switchUnits(ctx context.Context, runner processRunner, action string, units []string) error {
	args := make([]string, 0, len(units)+2)
	args = append(args, "--user", action)
	args = append(args, units...)
	return runner.Run(ctx, "systemctl", args...)
}
