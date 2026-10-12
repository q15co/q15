// Package q15cli implements the q15 management CLI: the single user-facing
// entry point that renders and drives a rootless podman stack.
//
// The package imports the agent's own configuration type and validation, so a
// configuration the CLI writes is one the agent accepts; that is why the CLI
// lives inside the agent module rather than in one of its own. Dispatch stays
// on the standard library: a verb is looked up and handed its own arguments,
// and no CLI framework is involved.
package q15cli

import (
	"fmt"
	"io"
)

// Run executes a single CLI invocation.
//
// release is the identifier the binary was built with, and only `version`
// reports it, verbatim. stdout carries a command's output. Bare `q15` prints
// the command list to stdout and reports success, because a bare invocation is
// a request for help; an unrecognised command prints the same list to stderr
// and returns an error.
//
// Run takes no context on purpose: every command here answers immediately.
// The verbs that shell out to systemd and podman take one when they arrive.
func Run(release string, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		writeUsage(stdout)
		return nil
	}

	switch args[0] {
	case "version":
		return runVersion(release, args[1:], stdout)
	case "help", "-h", "--help":
		writeUsage(stdout)
		return nil
	default:
		writeUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// writeUsage prints the command list. It goes to stdout for a requested help
// and to stderr for an unrecognised command.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `q15 renders and drives a q15 stack.

usage:
  q15 <command> [arguments]

commands:
  version   print the release, configuration schema and state layout versions
  help      print this message

The install commands (init, up, down, status, logs, doctor, secret, origin)
arrive with the install slice; run 'q15 version' to see what this build is.
`)
}
