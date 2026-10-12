// Package command holds what a command runs against: the process seam it is
// handed, and the error classification its failure is reported with. The two
// belong together, because both are the boundary between a command and the
// process around it.
//
// A command never reaches a global. It is handed an Env, so a test supplies its
// own streams and no command needs a terminal, podman or systemd to be
// exercised. The commands that shell out add their seam here as well - an exec
// function, a filesystem root, an environment lookup - rather than calling
// os/exec or os.Getenv directly, which keeps this struct the one place a
// command touches the process.
package command

import (
	"io"
	"strings"
)

// Env is the seam a command reaches the outside world through.
type Env struct {
	// Stdin is where an interactive prompt reads. Nothing reads it yet: the
	// command that prompts is `init`, and it prompts only on a terminal.
	Stdin io.Reader
	// Stdout carries a command's output and nothing else, because a caller may
	// parse it. Machine-readable output stays byte-exact on this stream.
	Stdout io.Writer
	// Stderr carries everything human: progress, prompts and diagnostics.
	Stderr io.Writer
	// Release is the identifier the binary was built with, injected at link
	// time by the release workflow. Only `version` reports it, verbatim.
	Release string
}

// WithDefaults returns the Env with a missing stream replaced by an empty one,
// so no command treats an absent stream as a special case and none can panic
// writing to a nil writer. Run applies it once, before a command is called; a
// command's own code never has to.
func (e Env) WithDefaults() Env {
	if e.Stdin == nil {
		e.Stdin = strings.NewReader("")
	}
	if e.Stdout == nil {
		e.Stdout = io.Discard
	}
	if e.Stderr == nil {
		e.Stderr = io.Discard
	}
	return e
}
