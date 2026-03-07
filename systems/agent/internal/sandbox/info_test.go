package sandbox

import (
	"strings"
	"testing"
)

func TestApplySandboxProbeOutputParsesOSNixAndBash(t *testing.T) {
	info := Info{}

	applySandboxProbeOutput(
		&info,
		strings.Join([]string{
			"os_id=debian",
			"os_version_id=12",
			"os_pretty_name=Debian GNU/Linux 12 (bookworm)",
			"nix_path=/root/.nix-profile/bin/nix",
			"nix_version=nix (Nix) 2.33.3",
			"bash_path=/bin/bash",
			"bash_version=GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu)",
		}, "\n"),
	)

	if got, want := info.OSID, "debian"; got != want {
		t.Fatalf("OSID = %q, want %q", got, want)
	}
	if got, want := info.OSVersionID, "12"; got != want {
		t.Fatalf("OSVersionID = %q, want %q", got, want)
	}
	if got, want := info.OSPrettyName, "Debian GNU/Linux 12 (bookworm)"; got != want {
		t.Fatalf("OSPrettyName = %q, want %q", got, want)
	}
	if got, want := info.NixPath, "/root/.nix-profile/bin/nix"; got != want {
		t.Fatalf("NixPath = %q, want %q", got, want)
	}
	if got, want := info.NixVersion, "nix (Nix) 2.33.3"; got != want {
		t.Fatalf("NixVersion = %q, want %q", got, want)
	}
	if got, want := info.BashPath, "/bin/bash"; got != want {
		t.Fatalf("BashPath = %q, want %q", got, want)
	}
	if got, want := info.BashVersion, "GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu)"; got != want {
		t.Fatalf("BashVersion = %q, want %q", got, want)
	}
}

func TestApplySandboxProbeOutputIgnoresUnknownAndMalformed(t *testing.T) {
	info := Info{}

	applySandboxProbeOutput(
		&info,
		strings.Join([]string{
			"nix_path=/nix/var/nix/profiles/default/bin/nix",
			"this-is-not-a-kv-line",
			"unknown_key=ignored",
			"=missing_key",
		}, "\n"),
	)

	if got, want := info.NixPath, "/nix/var/nix/profiles/default/bin/nix"; got != want {
		t.Fatalf("NixPath = %q, want %q", got, want)
	}
	if info.BashPath != "" || info.BashVersion != "" || info.OSID != "" {
		t.Fatalf("unexpected fields parsed: %#v", info)
	}
}

func TestApplySandboxProbeOutputHandlesMissingOptionalRuntimeFields(t *testing.T) {
	info := Info{}

	applySandboxProbeOutput(
		&info,
		strings.Join([]string{
			"os_id=debian",
			"os_version_id=12",
		}, "\n"),
	)

	if got, want := info.OSID, "debian"; got != want {
		t.Fatalf("OSID = %q, want %q", got, want)
	}
	if got, want := info.OSVersionID, "12"; got != want {
		t.Fatalf("OSVersionID = %q, want %q", got, want)
	}
	if info.NixPath != "" || info.NixVersion != "" || info.BashPath != "" ||
		info.BashVersion != "" {
		t.Fatalf("expected missing optional runtime fields, got %#v", info)
	}
}

func TestSandboxProbeCommandIncludesMinimalRuntimeChecks(t *testing.T) {
	cmd := sandboxProbeCommand()
	for _, want := range []string{
		"os_id=",
		"os_version_id=",
		"os_pretty_name=",
		"nix_path=",
		"nix_version=",
		"bash_path=",
		"bash_version=",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("probe command missing %q: %q", want, cmd)
		}
	}

	for _, disallowed := range []string{
		"package_manager=",
		"tool=",
		"shell=",
		"for c in apt-get apk pacman",
		"for c in git curl",
	} {
		if strings.Contains(cmd, disallowed) {
			t.Fatalf("probe command should not include %q: %q", disallowed, cmd)
		}
	}
}
