package q15cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
)

func TestRunWithoutArgumentsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Run("dev", nil, &stdout, &stderr); err != nil {
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

			if err := Run("dev", []string{arg}, &stdout, &stderr); err != nil {
				t.Fatalf("Run(%q) error = %v", arg, err)
			}
			if !strings.Contains(stdout.String(), "commands:") {
				t.Fatalf("stdout = %q, want the command list", stdout.String())
			}
		})
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := Run("dev", []string{"bogus"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run() error = nil, want an unknown-command error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %q, want it to name the command", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

// TestRunVersion pins the whole output, not just the release: the three
// identifiers are what a bug report quotes, and their order is the contract.
func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if err := Run("v1.2.3", []string{"version"}, &stdout, &stderr); err != nil {
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

	if err := Run("dev", []string{"version", "extra"}, &stdout, &stderr); err == nil {
		t.Fatal("Run() error = nil, want a rejection")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
