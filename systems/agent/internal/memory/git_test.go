package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommitterEnsureRepoAddsSafeDirectory(t *testing.T) {
	t.Setenv("FAKE_GIT_SAFE_OUTPUT", "")

	committer, logPath := newFakeGitCommitter(t)
	repoDir := filepath.Join(t.TempDir(), "memory")

	if err := committer.EnsureRepo(context.Background(), repoDir); err != nil {
		t.Fatalf("EnsureRepo() error = %v", err)
	}

	got := readFakeGitLog(t, logPath)
	want := []string{
		"config --global --get-all safe.directory",
		"config --global --add safe.directory " + repoDir,
		"-C " + repoDir + " rev-parse --is-inside-work-tree",
		"-C " + repoDir + " init",
		"-C " + repoDir + " config user.name " + defaultGitUserName,
		"-C " + repoDir + " config user.email " + defaultGitUserEmail,
		"-C " + repoDir + " config commit.gpgsign false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("EnsureRepo() commands = %q, want %q", got, want)
	}
}

func TestGitCommitterEnsureRepoSkipsExistingSafeDirectory(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "memory")
	t.Setenv("FAKE_GIT_SAFE_OUTPUT", repoDir)

	committer, logPath := newFakeGitCommitter(t)
	if err := committer.EnsureRepo(context.Background(), repoDir); err != nil {
		t.Fatalf("EnsureRepo() error = %v", err)
	}

	got := readFakeGitLog(t, logPath)
	for _, line := range got {
		if strings.Contains(line, "--add safe.directory") {
			t.Fatalf("EnsureRepo() unexpectedly added safe.directory: %q", got)
		}
	}
}

func newFakeGitCommitter(t *testing.T) (*GitCommitter, string) {
	t.Helper()

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "git.log")
	scriptPath := filepath.Join(tempDir, "git")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FAKE_GIT_LOG"
if [ "$1" = "config" ] && [ "$2" = "--global" ] && [ "$3" = "--get-all" ] && [ "$4" = "safe.directory" ]; then
  if [ -n "${FAKE_GIT_SAFE_OUTPUT:-}" ]; then
    printf '%s\n' "$FAKE_GIT_SAFE_OUTPUT"
    exit 0
  fi
  exit 1
fi
if [ "$1" = "config" ] && [ "$2" = "--global" ] && [ "$3" = "--add" ] && [ "$4" = "safe.directory" ]; then
  exit 0
fi
if [ "$1" = "-C" ] && [ "$3" = "rev-parse" ] && [ "$4" = "--is-inside-work-tree" ]; then
  exit 1
fi
if [ "$1" = "-C" ] && [ "$3" = "init" ]; then
  exit 0
fi
if [ "$1" = "-C" ] && [ "$3" = "config" ]; then
  exit 0
fi
printf 'unexpected args: %s\n' "$*" >&2
exit 2
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", scriptPath, err)
	}

	t.Setenv("FAKE_GIT_LOG", logPath)

	return &GitCommitter{bin: scriptPath}, logPath
}

func readFakeGitLog(t *testing.T, logPath string) []string {
	t.Helper()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", logPath, err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}
