package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

// runMemory is the read-only memory surface: reports drawn from the agent's
// memory files on this host.
//
// Memory is files, so anything that can read the directory can report on it,
// with no running agent and no podman. Nothing here writes into the root: the
// verbs read through the store's load methods and never call Store.Init.
//
// The reports judge file sizes against the configuration defaults rather than
// a loaded configuration. The rendered agent configuration's host path is an
// internal detail of stack rendering (the install slice that writes it has
// not landed), and resolving a loaded configuration would drag in the
// container's environment indirections, which an operator's shell does not
// carry. The fallback is the same defaults config resolution applies, and
// every sized line of the report prints the limit it was judged against, so a
// fallback is never silent.
func runMemory(ctx context.Context, env command.Env, args []string) error {
	if len(args) == 0 || asksForHelp(args[0]) {
		writeMemoryUsage(env.Stdout)
		return nil
	}

	switch args[0] {
	case "status":
		return runMemoryStatus(ctx, env, args[1:])
	default:
		return command.Usagef("unknown memory command %q", args[0])
	}
}

// writeMemoryUsage prints what the memory surface can do, and the one flag an
// operator has to name correctly before any of it is readable.
func writeMemoryUsage(w io.Writer) {
	fmt.Fprint(w, `q15 memory reports on the agent's memory files on this host.

Read-only surface: nothing here writes to the memory root, and no part of the
report needs the stack to run.

usage:
  q15 memory status --memory-root PATH
                                head seq, checkpoint lag and age, cognition job
                                failures, file sizes against their budgets and
                                caps, and the conversion ledger

--memory-root is required: the agent's /memory directory,
the stack's memory volume, on this host. --stack and
--state-dir name the agent the way the other verbs do; the report still comes
straight from the memory root.
`)
}

// memoryFlags are the flags a memory verb takes: the stack flags every verb
// shares, and the memory root the report reads.
type memoryFlags struct {
	stackFlags
	memoryRoot string
}

// newMemoryFlagSet returns the flag set for one memory verb. The memory root
// has no usable default: a stack's memory volume is not derivable from the
// stack's state directory, and guessing a podman-internal path is exactly the
// guess this verb refuses to make.
func newMemoryFlagSet(name string, env command.Env, flags *memoryFlags) *flag.FlagSet {
	fs := newStackFlagSet(name, env, &flags.stackFlags)
	fs.StringVar(
		&flags.memoryRoot,
		"memory-root",
		"",
		"the agent's /memory directory, the stack's memory volume, on this host (required)",
	)
	return fs
}

// defaultStatusLimits returns the byte budgets the report judges sizes
// against; see the note on runMemory for why they are the defaults.
func defaultStatusLimits() memory.StatusLimits {
	return memory.StatusLimits{
		WorkingMemoryBudgetBytes: config.DefaultMemoryWorkingBudgetBytes,
		OpenLoopsBudgetBytes:     config.DefaultMemoryOpenLoopsBudgetBytes,
	}
}

// runMemoryStatus reports one memory root: head, checkpoint lag, job failures,
// file sizes and the ledger. stdout carries the report and nothing else, and
// the process does not fail because a report says something is wrong: a
// stalled conversion is data, and whoever reads the report reacts to it.
func runMemoryStatus(ctx context.Context, env command.Env, args []string) error {
	flags := memoryFlags{}
	fs := newMemoryFlagSet("memory status", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("memory status takes no arguments, got %q", fs.Arg(0))
	}
	if flags.memoryRoot == "" {
		return command.Usagef(
			"memory status needs --memory-root: the agent's /memory directory, the stack's memory volume, on this host",
		)
	}

	info, err := os.Stat(flags.memoryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"memory status: --memory-root %s does not exist: it is the agent's /memory directory, the stack's memory volume, on this host",
				flags.memoryRoot,
			)
		}
		return fmt.Errorf("memory status: read --memory-root %s: %w", flags.memoryRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"memory status: --memory-root %s is a file, not the memory directory",
			flags.memoryRoot,
		)
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}

	store := memory.NewStore(memoryrepo.New(flags.memoryRoot, nil), profile.AgentName)
	status, err := memory.CollectStatus(ctx, store, defaultStatusLimits(), time.Now())
	if err != nil {
		return fmt.Errorf("memory status: %w", err)
	}
	return memory.RenderStatus(env.Stdout, status)
}
