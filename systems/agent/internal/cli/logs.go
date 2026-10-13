package cli

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/command"
)

func runLogs(ctx context.Context, env command.Env, args []string) error {
	flags := stackFlags{}
	fs := newStackFlagSet("logs", env, &flags)
	follow := fs.Bool("follow", false, "keep printing lines as they arrive")
	fs.BoolVar(follow, "f", false, "shorthand for -follow")
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}

	words := make([]string, 0, len(fs.Args()))
	for _, arg := range fs.Args() {
		// The standard library stops parsing at the first word that is not a
		// flag, so `logs q15-agent -f` arrives here with the flag still among
		// the positional words. Reading it costs three lines and saves the
		// operator a rule they would have to be told.
		if arg == "-f" || arg == "--follow" {
			*follow = true
			continue
		}
		words = append(words, arg)
	}
	if len(words) > 1 {
		return command.Usagef("logs takes at most one service, got %q", words[1])
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}
	service := ""
	if len(words) == 1 {
		service = words[0]
	}
	return lifecycleFor(profile, env).Logs(ctx, service, *follow)
}
