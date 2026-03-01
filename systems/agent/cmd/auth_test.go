package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandIncludesAuthCommand(t *testing.T) {
	found := false
	for _, command := range rootCmd.Commands() {
		if command.Name() == "auth" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("root command does not include auth command")
	}
}

func TestAuthLoginRequiresProvider(t *testing.T) {
	cmd := newAuthLoginCommand()
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error when --provider is missing")
	}
	if !strings.Contains(err.Error(), "required flag(s) \"provider\" not set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthStatusRejectsUnsupportedProvider(t *testing.T) {
	cmd := newAuthStatusCommand()
	cmd.SetArgs([]string{"--provider", "anthropic"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthLogoutRejectsUnsupportedProvider(t *testing.T) {
	cmd := newAuthLogoutCommand()
	cmd.SetArgs([]string{"--provider", "anthropic"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}
