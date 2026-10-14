package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// TestMemoryWithoutCommandPrintsUsage checks the bare group: like the secret
// verbs, asking for the memory surface without a subcommand is a request for
// its usage, and that usage names the one flag every memory report needs.
func TestMemoryWithoutCommandPrintsUsage(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	if err := Run(context.Background(), env, []string{"memory"}); err != nil {
		t.Fatalf("Run(memory) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "q15 memory status") {
		t.Fatalf("stdout = %q, want the memory usage", stdout.String())
	}
}

// TestMemoryUnknownCommandIsAUsageError pins the shape error for a word the
// memory surface does not handle.
func TestMemoryUnknownCommandIsAUsageError(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "bogus"})
	if err == nil {
		t.Fatal("Run(memory bogus) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusRequiresMemoryRoot is the one thing an operator must get
// right: the verb refuses to guess a podman-internal path, and its usage error
// says what --memory-root names.
func TestMemoryStatusRequiresMemoryRoot(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "status"})
	if err == nil {
		t.Fatal("Run(memory status) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "--memory-root") {
		t.Fatalf("error = %q, want it to name the flag", err.Error())
	}
	if !strings.Contains(err.Error(), "memory volume") {
		t.Fatalf("error = %q, want it to say what the directory is", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusUnknownFlagIsAUsageError covers the flag parser: an unknown
// flag is a shape mistake like everywhere else in this command line.
func TestMemoryStatusUnknownFlagIsAUsageError(t *testing.T) {
	env, _, stderr := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "status", "--bogus"})
	if err == nil {
		t.Fatal("Run(memory status --bogus) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Fatalf("stderr = %q, want the flag named", stderr.String())
	}
}

// TestMemoryStatusRejectsArguments pins the no-arguments shape of the verb:
// everything it reports comes from --memory-root and the flags.
func TestMemoryStatusRejectsArguments(t *testing.T) {
	env, _, _ := newEnv(&fakeExec{})

	err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "extra"},
	)
	if err == nil {
		t.Fatal("Run(memory status extra) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("error = %q, want it to reject the argument", err.Error())
	}
}

// TestMemoryStatusNamesAMissingRoot is the common typo: a path that is not
// there must read as a wrong --memory-root, not as a stack trace.
func TestMemoryStatusNamesAMissingRoot(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})
	root := filepath.Join(t.TempDir(), "absent")

	err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "--memory-root", root},
	)
	if err == nil {
		t.Fatal("Run(memory status --memory-root absent) error = nil, want a failure")
	}
	if got := command.ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(error) = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %q, want it to name the missing root", err.Error())
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error = %q, want it to echo the path", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusReportsABareRoot is the thin success case: stdout carries a
// report drawn from the memory root, stderr stays quiet, and the verb runs
// nothing outside the process — no podman, no systemctl, no git.
func TestMemoryStatusReportsABareRoot(t *testing.T) {
	// A default state directory is resolved even though the report never reads
	// it, so point it at the test's own tree rather than the host's.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	fake := &fakeExec{}
	env, stdout, stderr := newEnv(fake)

	stateDir := filepath.Join("history", "state")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, stateDir), 0o755); err != nil {
		t.Fatalf("create head state dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, stateDir, "head.json"),
		[]byte(`{"last_seq":12,"updated_at":"2026-10-13T09:10:33Z"}`),
		0o644,
	); err != nil {
		t.Fatalf("write head state: %v", err)
	}

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "--memory-root", root},
	); err != nil {
		t.Fatalf("Run(memory status) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "head seq 12") {
		t.Fatalf("stdout = %q, want the head reported", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if len(fake.calls) != 0 {
		t.Fatalf("calls = %v, want the verb to run nothing", fake.calls)
	}
}
