package app

import (
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/sandbox"
)

func TestComposeSystemPromptIncludesOSRuntimeAndNixBashDetails(t *testing.T) {
	info := sandbox.SandboxInfo{
		ContainerName: "q15-jared",
		WorkspaceDir:  "/workspace",
		Runtime:       "nix-only",
		BaseImage:     "docker.io/library/debian:bookworm-slim",
		OSPrettyName:  "Debian GNU/Linux 12 (bookworm)",
		NixPath:       "/root/.nix-profile/bin/nix",
		NixVersion:    "nix (Nix) 2.33.3",
		BashPath:      "/bin/bash",
		BashVersion:   "GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu)",
	}

	prompt := composeSystemPrompt("Base prompt", "Jared", info, "/memory")

	for _, want := range []string{
		"Sandbox Environment (authoritative runtime info):",
		"- OS: Debian GNU/Linux 12 (bookworm)",
		"- Sandbox runtime: nix-only",
		"- Base image: docker.io/library/debian:bookworm-slim",
		"- Nix: /root/.nix-profile/bin/nix (nix (Nix) 2.33.3)",
		"- Bash: /bin/bash (GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu))",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	for _, notWanted := range []string{
		"- Detected package manager binary:",
		"- Available shells:",
		"- Preinstalled tools detected:",
	} {
		if strings.Contains(prompt, notWanted) {
			t.Fatalf("prompt should not contain %q:\n%s", notWanted, prompt)
		}
	}
}

func TestFormatBinarySummarySupportsPartialValues(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		version string
		want    string
	}{
		{
			name:    "path_and_version",
			path:    "/bin/bash",
			version: "GNU bash, version 5.2",
			want:    "/bin/bash (GNU bash, version 5.2)",
		},
		{
			name:    "path_only",
			path:    "/bin/bash",
			version: "",
			want:    "/bin/bash",
		},
		{
			name:    "version_only",
			path:    "",
			version: "nix (Nix) 2.33.3",
			want:    "nix (Nix) 2.33.3",
		},
		{
			name:    "empty",
			path:    "",
			version: "",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBinarySummary(tc.path, tc.version); got != tc.want {
				t.Fatalf(
					"formatBinarySummary(%q, %q) = %q, want %q",
					tc.path,
					tc.version,
					got,
					tc.want,
				)
			}
		})
	}
}
