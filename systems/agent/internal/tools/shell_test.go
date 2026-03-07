package tools

import (
	"context"
	"strings"
	"testing"
)

type recordingExec struct {
	lastCommand  string
	lastPackages []string
}

func (r *recordingExec) ExecNixShellBash(
	_ context.Context,
	command string,
	packages []string,
) (string, error) {
	r.lastCommand = command
	r.lastPackages = append([]string(nil), packages...)
	return "ok", nil
}

func TestNixShellBashRunRejectsMissingPackages(t *testing.T) {
	shell := NewNixShellBash(&recordingExec{})

	_, err := shell.Run(context.Background(), `{"command":"echo hi"}`)
	if err == nil {
		t.Fatalf("expected missing packages error")
	}
	if !strings.Contains(err.Error(), "missing required argument: packages") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNixShellBashRunRejectsEmptyPackageEntries(t *testing.T) {
	shell := NewNixShellBash(&recordingExec{})

	_, err := shell.Run(
		context.Background(),
		`{"command":"echo hi","packages":["nixpkgs#git"," "]}`,
	)
	if err == nil {
		t.Fatalf("expected empty package error")
	}
	if !strings.Contains(err.Error(), "packages[1] must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNixShellBashRunForwardsStructuredExecNixShellBashRequest(t *testing.T) {
	rec := &recordingExec{}
	shell := NewNixShellBash(rec)

	out, err := shell.Run(
		context.Background(),
		`{"command":"git --version","packages":["nixpkgs#git"]}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("Run() output = %q, want %q", out, "ok")
	}
	if got, want := rec.lastCommand, "git --version"; got != want {
		t.Fatalf("forwarded command = %q, want %q", got, want)
	}
	if got, want := strings.Join(rec.lastPackages, ","), "nixpkgs#git"; got != want {
		t.Fatalf("forwarded packages = %q, want %q", got, want)
	}
}
