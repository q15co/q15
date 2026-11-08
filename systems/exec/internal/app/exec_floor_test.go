package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecDockerfileUsesRepositoryFloorDefinition(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "..")
	sourcePath := filepath.Join(root, "docker", "exec-floor.txt")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", sourcePath, err)
	}
	if strings.TrimSpace(string(source)) == "" {
		t.Fatalf("%q is empty", sourcePath)
	}

	dockerPath := filepath.Join(root, "docker", "exec.Dockerfile")
	raw, err := os.ReadFile(dockerPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", dockerPath, err)
	}
	dockerfile := string(raw)
	for _, want := range []string{
		"RUN --mount=type=bind,source=docker/exec-floor.txt,target=/tmp/q15-exec-floor.txt",
		"done < /tmp/q15-exec-floor.txt",
		"FLOOR_HASH=\"$(sha256sum /tmp/q15-exec-floor.txt)\"",
		"/nix/var/nix/profiles/q15-floor/bin/readlink -f /nix/var/nix/profiles/q15-floor",
		`[ "$#" -eq 2 ] && [ -n "$1" ] && [ -n "$2" ]`,
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("%q does not contain %q", dockerPath, want)
		}
	}
}
