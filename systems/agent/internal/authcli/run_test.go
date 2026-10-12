package authcli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/q15co/q15/systems/agent/internal/command"
)

// TestRunPrintsUsageWithNoArgs pins the help path: a bare invocation is a
// request for help, so the usage goes to stdout and the status is 0.
func TestRunPrintsUsageWithNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := command.Env{Stdout: &stdout, Stderr: &stderr}
	if err := Run(context.Background(), env, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "q15 auth login") {
		t.Fatalf("unexpected usage output: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestRunUnknownCommandIsAUsageError keeps the exit status meaningful: a script
// that mistypes a verb has to be able to tell that from a command that failed.
func TestRunUnknownCommandIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	env := command.Env{Stdout: &stdout, Stderr: &stderr}
	err := Run(context.Background(), env, []string{"bogus"})
	if err == nil {
		t.Fatal("Run() error = nil, want an unknown-command error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "q15 auth login") {
		t.Fatalf("stderr = %q, want the usage", stderr.String())
	}
}

// TestRunRejectedFlagIsAUsageError covers the inner layer: the flag package
// reports an unknown flag as an ordinary error, so the package marks it.
func TestRunRejectedFlagIsAUsageError(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")

	var stdout, stderr bytes.Buffer
	env := command.Env{Stdout: &stdout, Stderr: &stderr}
	err := Run(
		context.Background(),
		env,
		[]string{"status", "--auth-path", authPath, "--nope"},
	)
	if err == nil {
		t.Fatal("Run() error = nil, want a flag error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunStatusReadsConfiguredAuthPath(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := q15auth.SetStorePath(authPath); err != nil {
		t.Fatalf("SetStorePath() error = %v", err)
	}
	if err := q15auth.SetCredential("openai", &q15auth.Credential{
		AccessToken: "tok-123",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	env := command.Env{Stdout: &stdout, Stderr: &stderr}
	if err := Run(
		context.Background(),
		env,
		[]string{"status", "--auth-path", authPath},
	); err != nil {
		t.Fatalf("Run(status) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "openai:") {
		t.Fatalf("unexpected status output: %q", stdout.String())
	}
}

func TestRunLogoutDeletesCredentials(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := q15auth.SetStorePath(authPath); err != nil {
		t.Fatalf("SetStorePath() error = %v", err)
	}
	if err := q15auth.SetCredential("openai", &q15auth.Credential{
		AccessToken: "tok-123",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	env := command.Env{Stdout: &stdout, Stderr: &stderr}
	if err := Run(
		context.Background(),
		env,
		[]string{"logout", "--auth-path", authPath},
	); err != nil {
		t.Fatalf("Run(logout) error = %v", err)
	}

	if err := q15auth.SetStorePath(authPath); err != nil {
		t.Fatalf("SetStorePath(second) error = %v", err)
	}
	store, err := q15auth.LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	if len(store.Credentials) != 0 {
		t.Fatalf("expected empty credentials after logout, got %#v", store.Credentials)
	}
}
