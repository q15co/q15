package authcli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
)

func TestRunPrintsUsageWithNoArgs(t *testing.T) {
	var stdout bytes.Buffer
	if err := Run(context.Background(), nil, &stdout, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "q15-auth login") {
		t.Fatalf("unexpected usage output: %q", stdout.String())
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

	var stdout bytes.Buffer
	if err := Run(
		context.Background(),
		[]string{"status", "--auth-path", authPath},
		&stdout,
		&stdout,
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

	var stdout bytes.Buffer
	if err := Run(
		context.Background(),
		[]string{"logout", "--auth-path", authPath},
		&stdout,
		&stdout,
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
