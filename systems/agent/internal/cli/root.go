// Package cli implements the q15 command line: the single user-facing entry
// point that renders and drives a rootless podman stack.
//
// The package imports the agent's own configuration type and validation, so a
// configuration the CLI writes is one the agent accepts; that is why the CLI
// lives inside the agent module rather than in one of its own. Dispatch stays on
// the standard library and on the registry in verbs.go: a verb is looked up
// there and handed the arguments that follow its name, no CLI framework is
// involved, and the help is generated from that same list rather than written
// out beside it.
package cli

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// Run executes a single invocation of the command line.
//
// env carries the streams and the release the binary was built with, so nothing
// here reaches a global. Bare `q15` prints the command list to stdout and
// reports success, because a bare invocation is a request for help; an
// unrecognised command prints the same list to stderr and returns a usage error,
// which main turns into exit status 2 rather than the 1 a failed command gets.
//
// `--version` and `-V` are answered here rather than as verbs, the same way the
// spellings that ask for help are: they are what a person types first, and the
// answer is the three lines `version` prints. `-V` and not `-v`, because `-v` is
// the conventional shorthand for verbose and nothing here is verbose.
func Run(ctx context.Context, env command.Env, args []string) error {
	env = env.WithDefaults()

	if len(args) == 0 || asksForHelp(args[0]) {
		writeUsage(env.Stdout)
		return nil
	}
	if asksForVersion(args[0]) {
		// Anything after the flag is still the verb's argument to refuse, so a
		// mistyped invocation fails where the mistake is.
		return runVersion(ctx, env, args[1:])
	}

	v, ok := lookup(args[0])
	if !ok {
		writeUsage(env.Stderr)
		return command.Usagef("unknown command %q", args[0])
	}
	return v.run(ctx, env, args[1:])
}

// asksForHelp reports whether a word asks for the command list rather than
// naming a verb.
func asksForHelp(word string) bool {
	return word == "help" || word == "-h" || word == "--help"
}

// asksForVersion reports whether a word asks for the release rather than naming
// a verb.
func asksForVersion(word string) bool {
	return word == "-V" || word == "--version"
}
