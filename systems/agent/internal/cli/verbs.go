package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// A verb is one thing this command line can do at its root: `version`, `auth`,
// and later `init`, `up`, `status`, `doctor`, `secret` and `origin`.
type verb struct {
	// name is the word that selects it.
	name string
	// summary is one line, and the generated help prints exactly this.
	summary string
	// run executes the verb with the arguments that follow its name. The Env it
	// receives is the one Run was given, with the missing streams already
	// defaulted, and ctx carries the caller's cancellation.
	run func(ctx context.Context, env command.Env, args []string) error
	// hidden keeps a verb out of the command list. It is for the surface podman
	// calls rather than a person: reachable, because something has to reach it,
	// and unlisted, because nobody should be told to type it.
	hidden bool
}

// verbs is the registry, in the order the help lists them.
//
// Dispatch, the generated help and the unknown-command error all read this
// list, so none of the three can drift from it: a verb is reachable, it is
// documented, and it is named in the error, or none of those. Each install verb
// adds one entry here beside its own file.
var verbs = []verb{
	{
		name:    "version",
		summary: "print the release, configuration schema and state layout versions",
		run:     runVersion,
	},
	{
		name:    "auth",
		summary: "manage the provider credentials the agent runs with",
		run:     runAuth,
	},
	{
		name:    "up",
		summary: "reload the user's systemd and start the stack",
		run:     runUp,
	},
	{
		name:    "down",
		summary: "stop the stack",
		run:     runDown,
	},
	{
		name:    "status",
		summary: "report what each unit and container of the stack is doing",
		run:     runStatus,
	},
	{
		name:    "memory",
		summary: "report conversion lag, failures and file budgets from the memory root",
		run:     runMemory,
	},
	{
		name:    "logs",
		summary: "print the journal of the stack, or of one service in it",
		run:     runLogs,
	},
	{
		name:    "secret",
		summary: "add a stack secret, list what podman knows, or re-provision a store",
		run:     runSecret,
	},
	{
		name:    "secret-driver",
		summary: "the four commands podman's shell secret driver calls",
		run:     runSecretDriver,
		hidden:  true,
	},
}

// lookup finds the verb a word selects.
func lookup(name string) (verb, bool) {
	for _, v := range verbs {
		if v.name == name {
			return v, true
		}
	}
	return verb{}, false
}

// writeUsage prints the command list, generated from the registry so a verb
// cannot be reachable but undocumented. `help` is not a verb: it is handled
// before the lookup, and it appears here because an operator looking for a
// command list should see the word that asks for one. The version spellings are
// named in the note under the list for the same reason: a flag the parser
// answers before the lookup is a word nobody can reach by reading the registry.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `q15 renders and drives a q15 stack.

usage:
  q15 <command> [arguments]

commands:
`)
	for _, v := range verbs {
		if v.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-8s  %s\n", v.name, v.summary)
	}
	fmt.Fprint(w, `  help      print this message

--version and -V print the same three lines the version command does, and --help
and -h print this list. The remaining install commands (init, doctor, origin)
arrive with the install slice; run 'q15 version' to see what this build is.
`)
}
