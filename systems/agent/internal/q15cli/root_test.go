package q15cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cli"
	"github.com/q15co/q15/systems/agent/internal/config"
)

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "dev"}
	if err := Run(context.Background(), env, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "usage:") {
		t.Fatalf("stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "dev"}
			if err := Run(context.Background(), env, []string{arg}); err != nil {
				t.Fatalf("Run(%q) error = %v", arg, err)
			}
			if !strings.Contains(stdout.String(), "commands:") {
				t.Fatalf("stdout = %q, want the command list", stdout.String())
			}
		})
	}
}

// TestRunUnknownCommandIsAUsageError pins the two things a script depends on:
// the command list goes to stderr with the message, and the error is classified
// as a shape mistake so the process exits 2 rather than 1.
func TestRunUnknownCommandIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "dev"}
	err := Run(context.Background(), env, []string{"bogus"})
	if err == nil {
		t.Fatal("Run() error = nil, want an unknown-command error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %q, want it to name the command", err)
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

// TestHelpListsEveryRegisteredCommand is the drift guard for the registry: a
// verb the lookup would run but the help never mentions is undiscoverable, and
// both read the same list precisely so they cannot disagree.
func TestHelpListsEveryRegisteredCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if len(commands) == 0 {
		t.Fatal("the registry is empty, want at least one verb")
	}

	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "dev"}
	if err := Run(context.Background(), env, []string{"help"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	help := stdout.String()
	for _, cmd := range commands {
		if !strings.Contains(help, cmd.name) {
			t.Fatalf("help = %q, want it to list %q", help, cmd.name)
		}
		if !strings.Contains(help, cmd.summary) {
			t.Fatalf("help = %q, want it to carry the summary of %q", help, cmd.name)
		}
		if _, ok := lookup(cmd.name); !ok {
			t.Fatalf("lookup(%q) = false, want a registered verb to be reachable", cmd.name)
		}
	}
	if !strings.Contains(help, "help      print this message") {
		t.Fatalf("help = %q, want the help word itself listed", help)
	}
}

// TestRunVersion pins the whole output, not just the release: the three
// identifiers are what a bug report quotes, and their order is the contract.
func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "v1.2.3"}
	if err := Run(context.Background(), env, []string{"version"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := fmt.Sprintf(
		"release: v1.2.3\nconfig_schema: %d\nstate_layout: %d\n",
		config.SchemaVersion,
		StateLayoutVersion,
	)
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersionRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := cli.Env{Stdout: &stdout, Stderr: &stderr, Release: "dev"}
	err := Run(context.Background(), env, []string{"version", "extra"})
	if err == nil {
		t.Fatal("Run() error = nil, want a rejection")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
