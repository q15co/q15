package exec

import (
	"regexp"
	"strings"
)

// nixNoisePatterns matches well-known benign nix bootstrap/fetch/store stderr
// lines that are emitted during first-run package resolution but carry no
// actionable information for the model or user.
var nixNoisePatterns = []*regexp.Regexp{
	// "these 10 paths will be fetched (120.5 MiB download, 450.2 MiB unpacked)"
	regexp.MustCompile(`^these \d+ path[s]? will be fetched`),
	// "copying path '/nix/store/...' from 'https://cache.nixos.org'..."
	regexp.MustCompile(`^copying path '/nix/store/[^\']*' from '[^']*'\.\.\.`),
	// "fetching 'https://cache.nixos.org/nar/...'..."
	regexp.MustCompile(`^fetching 'https?://[^']*'\.\.\.`),
	// "copying path '/nix/store/...'..."
	regexp.MustCompile(`^copying path '/nix/store/[^']*'\.\.\.`),
	// "building '/nix/store/xxx.drv'..."
	regexp.MustCompile(`^building '/nix/store/[^']*'\.\.\.`),
	// "evaluating derivation '...'"
	regexp.MustCompile(`^evaluating [^ ]+`),
	// "don't know how to build these paths:" followed by whitespace-only context
	regexp.MustCompile(`^don't know how to build these paths`),
	// "unpacking 'github:NixOS/nixpkgs/...' into the Git cache..."
	regexp.MustCompile(`^unpacking '[^']*' into the Git cache`),
	// "warning: the group 'nixbld' specified in 'build-users-group' does not exist"
	regexp.MustCompile(`^warning: the group '[^']*' specified in '[^']*' does not exist`),
	// Indented /nix/store/... path listings produced during fetch/unpack progress
	nixStorePathListing,
}

// nixStorePathListing matches indented /nix/store/... lines that nix prints
// during fetch/unpack progress, e.g. "  /nix/store/abc123-bash-5.2".
// This is checked against the original (untrimmed) line to preserve the
// indentation requirement — a bare /nix/store/ path is not noise.
var nixStorePathListing = regexp.MustCompile(`^\s+/nix/store/`)

// isNixNoiseLine reports whether a single line of stderr is known benign nix
// bootstrap/fetch/store chatter.
func isNixNoiseLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	// Store-path listings are checked against the original line so that
	// leading indentation is required — a bare store path is not noise.
	if nixStorePathListing.MatchString(line) {
		return true
	}
	trimmed := strings.TrimSpace(line)
	for _, re := range nixNoisePatterns {
		if re.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// filterNixBootstrapStderr removes known benign nix stderr chatter.
//
// On successful runs (exitCode == 0) it filters out lines that match known
// nix noise patterns. If the entire stderr consists of noise, it returns an
// empty string so that the "--- STDERR ---" section can be omitted entirely.
//
// On failed runs it returns stderr unchanged so that real diagnostics are
// never hidden.
func filterNixBootstrapStderr(stderr string, exitCode int32) string {
	if exitCode != 0 {
		return stderr
	}
	if stderr == "" {
		return ""
	}

	lines := strings.Split(stderr, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !isNixNoiseLine(line) {
			filtered = append(filtered, line)
		}
	}
	result := strings.Join(filtered, "\n")

	// Normalise trailing newline to match original behaviour.
	if strings.HasSuffix(stderr, "\n") && result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}

	return result
}
