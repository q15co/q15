package tools

import (
	"context"
	"strings"
	"testing"
)

type recordingExec struct {
	lastCommand string
}

func (r *recordingExec) Exec(_ context.Context, command string) (string, error) {
	r.lastCommand = command
	return "ok", nil
}

func TestShellRunRejectsMissingPackages(t *testing.T) {
	shell := NewShell(&recordingExec{})

	_, err := shell.Run(context.Background(), `{"command":"echo hi"}`)
	if err == nil {
		t.Fatalf("expected missing packages error")
	}
	if !strings.Contains(err.Error(), "missing required argument: packages") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShellRunRejectsEmptyPackageEntries(t *testing.T) {
	shell := NewShell(&recordingExec{})

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

func TestBuildNixShellExecCommand(t *testing.T) {
	got := buildNixShellExecCommand("echo 'hello world'", []string{"nixpkgs#git", "nixpkgs#jq"})

	if !strings.Contains(
		got,
		"nix --extra-experimental-features 'nix-command flakes' --option ssl-cert-file",
	) {
		t.Fatalf("missing nix flakes invocation: %q", got)
	}
	if !strings.Contains(
		got,
		"if [ -n \"${NIX_SSL_CERT_FILE:-}\" ] && [ ! -r \"${NIX_SSL_CERT_FILE}\" ]",
	) {
		t.Fatalf("missing NIX_SSL_CERT_FILE readability precheck: %q", got)
	}
	if !strings.Contains(
		got,
		"--option ssl-cert-file \"${NIX_SSL_CERT_FILE:-/etc/ssl/certs/ca-certificates.crt}\"",
	) {
		t.Fatalf("missing nix ssl-cert-file option: %q", got)
	}
	if !strings.Contains(got, "'nixpkgs#git' 'nixpkgs#jq'") {
		t.Fatalf("missing packages in generated command: %q", got)
	}
	if !strings.Contains(got, "--command /bin/bash -c") {
		t.Fatalf("missing shell command invocation: %q", got)
	}
	if !strings.Contains(got, `'echo '"'"'hello world'"'"''`) {
		t.Fatalf("missing quoted command payload: %q", got)
	}
}

func TestShellRunExecutesBuiltNixCommand(t *testing.T) {
	rec := &recordingExec{}
	shell := NewShell(rec)

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
	if !strings.Contains(
		rec.lastCommand,
		"nix --extra-experimental-features 'nix-command flakes' --option ssl-cert-file \"${NIX_SSL_CERT_FILE:-/etc/ssl/certs/ca-certificates.crt}\" shell 'nixpkgs#git'",
	) {
		t.Fatalf("unexpected executed command: %q", rec.lastCommand)
	}
}
