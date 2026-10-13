package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/stack"
)

// runLogs prints the journal of the stack, or of one service in it, and can
// follow it.
//
// The output is the command's data, so it goes to stdout unchanged: this verb
// selects and forwards, it does not summarise. With no service named every unit
// of the stack is read at once, which is what an operator wants when they do not
// yet know which part is unhappy.
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
	units := profile.ServiceNames()

	if len(words) == 1 {
		unit, err := resolveUnit(profile, units, words[0])
		if err != nil {
			return err
		}
		units = []string{unit}
	}

	journalArgs := make([]string, 0, len(units)*2+2)
	journalArgs = append(journalArgs, "--user")
	for _, unit := range units {
		journalArgs = append(journalArgs, "-u", unit)
	}
	if *follow {
		journalArgs = append(journalArgs, "--follow")
	}

	if err := env.Exec(ctx, "journalctl", journalArgs, env.Stdin, env.Stdout, env.Stderr); err != nil {
		if command.NotInstalled(err) {
			return fmt.Errorf("journalctl is not installed: %w", err)
		}
		return fmt.Errorf("read the journal: %w", err)
	}
	return nil
}

// resolveUnit turns the word an operator typed into one of the stack's units. A
// service word is the part that makes it recognisable - the name it has in the
// stack, or the same word with the stack's own prefix - and the full unit name
// works too, because that is what status prints and what an operator pastes
// back.
func resolveUnit(profile stack.Profile, units []string, word string) (string, error) {
	if stem, ok := strings.CutSuffix(word, ".container"); ok {
		word = stem + ".service"
	} else if stem, ok := strings.CutSuffix(word, ".network"); ok {
		word = stem + "-network.service"
	}
	prefix := profile.ResourceName() + "-"
	for _, unit := range units {
		stem := strings.TrimSuffix(strings.TrimSuffix(unit, "-network.service"), ".service")
		switch {
		case unit == word, stem == word, stem == prefix+word:
			return unit, nil
		}
	}
	return "", command.Usagef(
		"unknown service %q, want one of %s",
		word,
		strings.Join(serviceWords(profile, units), ", "),
	)
}

// serviceWords are the names resolveUnit accepts, for an error that lists them.
func serviceWords(profile stack.Profile, units []string) []string {
	prefix := profile.ResourceName() + "-"
	words := make([]string, 0, len(units))
	for _, unit := range units {
		stem := strings.TrimSuffix(strings.TrimSuffix(unit, "-network.service"), ".service")
		words = append(words, strings.TrimPrefix(stem, prefix))
	}
	return words
}
