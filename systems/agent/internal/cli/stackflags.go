package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/stack"
)

// stackFlags are the two flags every install verb shares.
//
// They are parsed by each verb rather than before dispatch, because the verb is
// what knows its own name, its own usage and its own arguments, and a parser in
// front of the lookup would have to guess which verb an unknown word belonged
// to. The values stay two strings here and become a Profile in exactly one
// place, so every verb names the same stack the same way.
type stackFlags struct {
	agentName string
	stateDir  string
}

// newStackFlagSet returns the flag set for one install verb. It reports on
// stderr and never on stdout, because stdout carries a verb's data and a usage
// mistake is not data. Parse reports an unknown flag or a bad value as an
// ordinary error, so a caller wraps it with command.AsUsage to say the command
// line was wrong in shape rather than that the work failed.
func newStackFlagSet(name string, env command.Env, flags *stackFlags) *flag.FlagSet {
	flags.agentName = stack.DefaultAgentName
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.StringVar(
		&flags.agentName,
		"stack",
		stack.DefaultAgentName,
		"the stack to act on, named as it was created",
	)
	fs.StringVar(
		&flags.stateDir,
		"state-dir",
		"",
		"where the stack's rendered state lives (default: the user's state directory)",
	)
	return fs
}

// profile builds the profile the flags name. An unset state directory resolves
// to the user's own here rather than at each call site, and the release is the
// one the binary was built with, which is where a rendered unit's image tags
// come from.
func (f stackFlags) profile(release string) (stack.Profile, error) {
	stateDir := f.stateDir
	if stateDir == "" {
		resolved, err := stack.DefaultStateDir()
		if err != nil {
			return stack.Profile{}, err
		}
		stateDir = resolved
	}
	profile := stack.DefaultProfile(release, stateDir)
	profile.AgentName = f.agentName
	return profile, nil
}

// processRunner runs a verb's external programs through the process seam it was
// handed, so reloading the user's systemd is exercised by a test rather than
// only on a real machine. Both streams go to stderr: what systemd says about a
// reload is a diagnostic, and stdout belongs to the verb's own output.
type processRunner struct{ env command.Env }

// Run implements stack.Runner.
func (r processRunner) Run(ctx context.Context, program string, args ...string) error {
	return r.env.Exec(ctx, program, args, nil, r.env.Stderr, r.env.Stderr)
}

// systemctlOutput runs one read-only systemctl query and returns what it wrote
// to standard output. systemctl reports state through its exit status as well as
// through its output, and "inactive" is an answer rather than a failure, so the
// caller reads the output and decides what to make of a non-zero status.
func systemctlOutput(ctx context.Context, env command.Env, args ...string) (string, error) {
	var out strings.Builder
	err := env.Exec(ctx, "systemctl", args, nil, &out, env.Stderr)
	if err != nil && command.NotInstalled(err) {
		return "", fmt.Errorf("systemctl is not installed: %w", err)
	}
	return out.String(), err
}
