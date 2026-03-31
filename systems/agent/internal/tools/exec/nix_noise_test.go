package exec

import (
	"strings"
	"testing"
)

func TestIsNixNoiseLine_MatchesKnownPatterns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		line  string
		match bool
	}{
		{
			name:  "paths will be fetched",
			line:  "these 10 paths will be fetched (120.5 MiB download, 450.2 MiB unpacked)",
			match: true,
		},
		{name: "singular path will be fetched", line: "these 1 paths will be fetched", match: true},
		{
			name:  "copying path from cache",
			line:  `copying path '/nix/store/x8f9a2b-foo-1.0' from 'https://cache.nixos.org'...`,
			match: true,
		},
		{
			name:  "copying path no source",
			line:  `copying path '/nix/store/x8f9a2b-foo-1.0'...`,
			match: true,
		},
		{
			name:  "fetching url",
			line:  `fetching 'https://cache.nixos.org/nar/abcdef1234'...`,
			match: true,
		},
		{
			name:  "building derivation",
			line:  `building '/nix/store/x8f9a2b-foo.drv'...`,
			match: true,
		},
		{
			name:  "evaluating",
			line:  "evaluating derivation '/nix/store/x8f9a2b-foo.drv'",
			match: true,
		},
		{name: "don't know how to build", line: "don't know how to build these paths", match: true},
		{
			name:  "unpacking into git cache",
			line:  "unpacking 'github:NixOS/nixpkgs/15c6719d8c604779cf59e03c245ea61d3d7ab69b' into the Git cache...",
			match: true,
		},
		{
			name:  "nixbld group warning",
			line:  "warning: the group 'nixbld' specified in 'build-users-group' does not exist",
			match: true,
		},
		{
			name:  "indented store path listing",
			line:  "  /nix/store/7qs31js7dw2ayj0q807v9apmwbw00jvb-cowsay-3.8.4",
			match: true,
		},
		{
			name:  "tab-indented store path",
			line:  "\t/nix/store/7qs31js7dw2ayj0q807v9apmwbw00jvb-cowsay-3.8.4",
			match: true,
		},
		{
			name:  "bare store path not noise",
			line:  "/nix/store/7qs31js7dw2ayj0q807v9apmwbw00jvb-cowsay-3.8.4",
			match: false,
		},
		{name: "empty line", line: "", match: false},
		{name: "whitespace only", line: "   ", match: false},
		{name: "real command error", line: "error: no such file or directory", match: false},
		{name: "real command warning", line: "warning: deprecated option --foo", match: false},
		{name: "compiler warning", line: "main.go:15:2: unused variable 'x'", match: false},
		{
			name:  "git stderr",
			line:  "error: Your local changes would be overwritten by merge.",
			match: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isNixNoiseLine(tc.line)
			if got != tc.match {
				t.Errorf("isNixNoiseLine(%q) = %v, want %v", tc.line, got, tc.match)
			}
		})
	}
}

func TestFilterNixBootstrapStderr_PreservesOnNonZeroExit(t *testing.T) {
	t.Parallel()

	noise := "these 10 paths will be fetched (120.5 MiB download, 450.2 MiB unpacked)\n"
	got := filterNixBootstrapStderr(noise, 1)
	if got != noise {
		t.Errorf(
			"filterNixBootstrapStderr on non-zero exit changed stderr:\ngot:  %q\nwant: %q",
			got,
			noise,
		)
	}
}

func TestFilterNixBootstrapStderr_FiltersNoiseOnSuccess(t *testing.T) {
	t.Parallel()

	stderr := strings.Join([]string{
		"these 10 paths will be fetched (120.5 MiB download, 450.2 MiB unpacked)",
		"copying path '/nix/store/abc123-bash-5.2' from 'https://cache.nixos.org'...",
		"fetching 'https://cache.nixos.org/nar/xyz789'...",
	}, "\n") + "\n"

	got := filterNixBootstrapStderr(stderr, 0)
	if strings.TrimSpace(got) != "" {
		t.Errorf("filterNixBootstrapStderr left content: %q", got)
	}
}

func TestFilterNixBootstrapStderr_PreservesRealStderrOnSuccess(t *testing.T) {
	t.Parallel()

	stderr := "these 10 paths will be fetched\nwarning: deprecated API used\n"

	got := filterNixBootstrapStderr(stderr, 0)
	if !strings.Contains(got, "warning: deprecated API used") {
		t.Errorf("filterNixBootstrapStderr removed real stderr, got: %q", got)
	}
	if strings.Contains(got, "these 10 paths will be fetched") {
		t.Errorf("filterNixBootstrapStderr kept noise, got: %q", got)
	}
}

func TestFilterNixBootstrapStderr_EmptyInput(t *testing.T) {
	t.Parallel()

	got := filterNixBootstrapStderr("", 0)
	if got != "" {
		t.Errorf("filterNixBootstrapStderr empty input returned %q", got)
	}
}

func TestFilterNixBootstrapStderr_PreservesTrailingNewline(t *testing.T) {
	t.Parallel()

	stderr := "real warning: something\n"
	got := filterNixBootstrapStderr(stderr, 0)
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("filterNixBootstrapStderr dropped trailing newline, got: %q", got)
	}
}

func TestFormatExecSessionResult_OmitsStderrSectionWhenAllNoise(t *testing.T) {
	t.Parallel()

	got := formatExecSessionResult(true, 0, "", "hello\n", "these 10 paths will be fetched\n")
	if strings.Contains(got, "--- STDERR ---") {
		t.Errorf("formatExecSessionResult should omit STDERR section when all noise:\n%s", got)
	}
	if !strings.Contains(got, "--- STDOUT ---") {
		t.Errorf("formatExecSessionResult should keep STDOUT section:\n%s", got)
	}
}

func TestFormatExecSessionResult_KeepsStderrSectionWithRealContent(t *testing.T) {
	t.Parallel()

	got := formatExecSessionResult(true, 0, "", "hello\n", "warning: deprecated\n")
	if !strings.Contains(got, "--- STDERR ---") {
		t.Errorf("formatExecSessionResult should keep STDERR section with real content:\n%s", got)
	}
	if !strings.Contains(got, "warning: deprecated") {
		t.Errorf("formatExecSessionResult should keep real stderr content:\n%s", got)
	}
}

func TestFormatExecSessionResult_KeepsAllStderrOnNonZeroExit(t *testing.T) {
	t.Parallel()

	got := formatExecSessionResult(true, 1, "", "", "these 10 paths will be fetched\nerror: boom\n")
	if !strings.Contains(got, "these 10 paths will be fetched") {
		t.Errorf("formatExecSessionResult should keep all stderr on non-zero exit:\n%s", got)
	}
	if !strings.Contains(got, "error: boom") {
		t.Errorf("formatExecSessionResult should keep error on non-zero exit:\n%s", got)
	}
}
