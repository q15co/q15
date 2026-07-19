package memoryrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommitterEnsureRepoAddsSafeDirectory(t *testing.T) {
	t.Setenv("FAKE_GIT_SAFE_OUTPUT", "")

	committer, logPath := newFakeGitCommitter(t)
	repoDir := filepath.Join(t.TempDir(), "state")

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
	repoDir := filepath.Join(t.TempDir(), "state")
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

func TestGitCommitterCommitPathsLeavesUnrelatedChangesOutOfCommit(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "commit.gpgsign", "false")

	jobPath := filepath.Join(repoDir, "domain", "jobs", "job.json")
	if err := os.MkdirAll(filepath.Dir(jobPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(job) error = %v", err)
	}
	if err := os.WriteFile(jobPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(job) error = %v", err)
	}
	notePath := filepath.Join(repoDir, "notes", "draft.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(note) error = %v", err)
	}
	if err := os.WriteFile(notePath, []byte("draft\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(note) error = %v", err)
	}
	runGit(t, repoDir, "add", "--", "notes/draft.md")

	committer := NewGitCommitter()
	if err := committer.CommitPaths(
		context.Background(),
		repoDir,
		"state: store job",
		[]string{"domain/jobs/job.json"},
	); err != nil {
		t.Fatalf("CommitPaths() error = %v", err)
	}

	committed := strings.TrimSpace(
		runGit(t, repoDir, "show", "--pretty=format:", "--name-only", "HEAD"),
	)
	if committed != "domain/jobs/job.json" {
		t.Fatalf("committed paths = %q, want scoped job only", committed)
	}
	staged := strings.TrimSpace(runGit(t, repoDir, "diff", "--cached", "--name-only"))
	if staged != "notes/draft.md" {
		t.Fatalf("staged paths = %q, want unrelated note preserved", staged)
	}
}

func TestGitCommitterResetPathsRestoresScopedIndex(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "test")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "commit.gpgsign", "false")

	jobPath := filepath.Join(repoDir, "domain", "jobs", "job.json")
	if err := os.MkdirAll(filepath.Dir(jobPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(job) error = %v", err)
	}
	if err := os.WriteFile(jobPath, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(job) error = %v", err)
	}
	runGit(t, repoDir, "add", "--", "domain/jobs/job.json")
	runGit(t, repoDir, "commit", "-m", "initial")

	if err := os.WriteFile(jobPath, []byte("after\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(job update) error = %v", err)
	}
	runGit(t, repoDir, "add", "--", "domain/jobs/job.json")

	committer := NewGitCommitter()
	if err := committer.ResetPaths(
		context.Background(),
		repoDir,
		[]string{"domain/jobs/job.json"},
	); err != nil {
		t.Fatalf("ResetPaths() error = %v", err)
	}
	if staged := strings.TrimSpace(
		runGit(t, repoDir, "diff", "--cached", "--name-only"),
	); staged != "" {
		t.Fatalf("staged paths after reset = %q, want none", staged)
	}
	if worktree := strings.TrimSpace(
		runGit(t, repoDir, "diff", "--name-only"),
	); worktree != "domain/jobs/job.json" {
		t.Fatalf("worktree paths after reset = %q, want job update preserved", worktree)
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

func runGit(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"git %q error = %v (%s)",
			strings.Join(args, " "),
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return string(out)
}
