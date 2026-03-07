package sandboxbuildah

import (
	"strings"
	"testing"
)

func TestNormalizeExecNixShellBashRequestRejectsMissingPackages(t *testing.T) {
	_, err := normalizeExecNixShellBashRequest(
		ExecNixShellBashRequest{Command: "echo hi"},
	)
	if err == nil {
		t.Fatal("expected missing packages error")
	}
	if !strings.Contains(err.Error(), "packages are required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeExecNixShellBashRequestRejectsEmptyPackageEntries(t *testing.T) {
	_, err := normalizeExecNixShellBashRequest(
		ExecNixShellBashRequest{
			Command:  "echo hi",
			Packages: []string{"nixpkgs#git", " "},
		},
	)
	if err == nil {
		t.Fatal("expected empty package error")
	}
	if !strings.Contains(err.Error(), "packages[1] must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildNixShellBashCommand(t *testing.T) {
	got := buildNixShellBashCommand("echo 'hello world'", []string{"nixpkgs#git", "nixpkgs#jq"})

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
