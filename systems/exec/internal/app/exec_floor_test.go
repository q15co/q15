package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExecFloorGeneratedCopyMatchesSource(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join("..", "..", "..", "..", "docker", "exec-floor.txt")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", sourcePath, err)
	}
	if execFloorDefinition != string(source) {
		t.Fatalf(
			"embedded exec-floor.txt differs from %q; run go generate ./systems/exec/internal/app",
			sourcePath,
		)
	}
}

func TestExecFloorDefinitionRetainsRequiredAttributes(t *testing.T) {
	t.Parallel()

	want := []string{
		"coreutils", "gnused", "gawk", "gnugrep", "findutils", "diffutils",
		"gnupatch", "gnutar", "gzip", "bzip2", "xz", "zstd", "unzip", "zip",
		"which", "file", "less", "procps", "hostname", "perl", "ripgrep", "jq",
	}
	var got []string
	for _, line := range strings.Split(execFloorDefinition, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		got = append(got, line)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("exec floor attributes = %v, want %v", got, want)
	}
}
