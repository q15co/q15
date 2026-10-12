// Package cli holds what the agent's command-line entry points share: the
// process seam a verb works through, and the error classification the process
// exit status follows.
//
// A verb never reaches a global. It is handed an Env, so a test supplies its
// own streams and no command needs a terminal, podman or systemd to be
// exercised. The verbs that shell out add their seam here as well - an exec
// function, a filesystem root, an environment lookup - rather than calling
// os/exec or os.Getenv directly, which keeps this struct the one place a
// command touches the process.
package cli

import (
	"io"
	"strings"
)

// Env is the seam a command reaches the outside world through.
type Env struct {
	// Stdin is where an interactive prompt reads. Nothing reads it yet: the
	// verb that prompts is `init`, and it prompts only on a terminal.
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
// so no verb treats an absent stream as a special case and none can panic
// writing to a nil writer. Run applies it once, before a verb is called; a
// verb's own code never has to.
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
