package cli

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/command"
)

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
	return lifecycleFor(profile, env).Stop(ctx)
}
