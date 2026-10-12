package q15cli

import (
	"context"
	"fmt"
	"io"

	"github.com/q15co/q15/systems/agent/internal/cli"
)

// command is one verb at the root of the CLI.
type command struct {
	// name is the word that selects the verb on the command line.
	name string
	// summary is one line, and the generated help prints exactly this.
	summary string
	// run executes the verb with the arguments that follow its name. The Env it
	// receives is the one Run was given, with the missing streams already
	// defaulted, and ctx carries the caller's cancellation.
	run func(ctx context.Context, env cli.Env, args []string) error
}

// commands is the registry, in the order the help lists the verbs.
//
// Dispatch, the generated help and the unknown-command error all read this
// list, so none of the three can drift from it: a verb is reachable, it is
// documented, and it is named in the error, or none of those. The install verbs
// (init, up, down, status, logs, doctor, secret, origin) each add one entry here
// beside their own file.
var commands = []command{
	{
		name:    "version",
		summary: "print the release, configuration schema and state layout versions",
		run:     runVersion,
	},
}

// lookup finds the verb a word selects.
func lookup(name string) (command, bool) {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

// writeUsage prints the command list, generated from the registry so a verb
// cannot be reachable but undocumented. `help` is not a verb: it is handled
// before the lookup, and it appears here because an operator looking for a
// command list should see the word that asks for one.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `q15 renders and drives a q15 stack.

usage:
  q15 <command> [arguments]

commands:
`)
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-8s  %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprint(w, `  help      print this message

The install commands (init, up, down, status, logs, doctor, secret, origin)
arrive with the install slice; run 'q15 version' to see what this build is.
`)
}
