// Package memory provides persistent agent memory storage and git-backed
// history for conversation state.
package memory

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultGitUserName  = "q15-memory"
	defaultGitUserEmail = "q15@local"
)

// Committer ensures the memory repository exists and records committed turns.
type Committer interface {
	EnsureRepo(ctx context.Context, repoDir string) error
	CommitAll(ctx context.Context, repoDir, message string) (string, error)
}

// GitCommitter records memory changes in a git repository.
type GitCommitter struct {
	bin string
}

// NewGitCommitter constructs a git-backed memory committer.
func NewGitCommitter() *GitCommitter {
	return &GitCommitter{bin: "git"}
}

// EnsureRepo initializes and configures the memory git repository when needed.
func (g *GitCommitter) EnsureRepo(ctx context.Context, repoDir string) error {
	if g == nil {
		return fmt.Errorf("nil git committer")
	}
	if err := g.ensureSafeDirectory(ctx, repoDir); err != nil {
		return err
	}

	_, err := g.run(ctx, repoDir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if _, initErr := g.run(ctx, repoDir, "init"); initErr != nil {
			return fmt.Errorf("initialize memory repo: %w", initErr)
		}
	}

	if _, err := g.run(ctx, repoDir, "config", "user.name", defaultGitUserName); err != nil {
		return fmt.Errorf("configure git user.name: %w", err)
	}
	if _, err := g.run(ctx, repoDir, "config", "user.email", defaultGitUserEmail); err != nil {
		return fmt.Errorf("configure git user.email: %w", err)
	}
	if _, err := g.run(ctx, repoDir, "config", "commit.gpgsign", "false"); err != nil {
		return fmt.Errorf("configure git commit.gpgsign: %w", err)
	}

	return nil
}

func (g *GitCommitter) ensureSafeDirectory(ctx context.Context, repoDir string) error {
	repoDir = filepath.Clean(strings.TrimSpace(repoDir))
	if repoDir == "" {
		return fmt.Errorf("memory repo dir is required")
	}

	out, err := g.runGlobal(ctx, "config", "--global", "--get-all", "safe.directory")
	if err != nil && !isGitConfigMissingValue(err) {
		return fmt.Errorf("read git safe.directory: %w", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if filepath.Clean(strings.TrimSpace(line)) == repoDir {
			return nil
		}
	}

	if _, err := g.runGlobal(ctx, "config", "--global", "--add", "safe.directory", repoDir); err != nil {
		return fmt.Errorf("configure git safe.directory: %w", err)
	}
	return nil
}

// CommitAll stages and commits all pending memory changes.
func (g *GitCommitter) CommitAll(ctx context.Context, repoDir, message string) (string, error) {
	if g == nil {
		return "", fmt.Errorf("nil git committer")
	}

	if _, err := g.run(ctx, repoDir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add memory changes: %w", err)
	}

	statusOut, err := g.run(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status memory changes: %w", err)
	}
	if strings.TrimSpace(statusOut) == "" {
		return "", nil
	}

	if _, err := g.run(ctx, repoDir, "commit", "--no-gpg-sign", "-m", message); err != nil {
		return "", fmt.Errorf("git commit memory changes: %w", err)
	}

	sha, err := g.run(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve memory commit sha: %w", err)
	}
	return strings.TrimSpace(sha), nil
}

func (g *GitCommitter) run(ctx context.Context, repoDir string, args ...string) (string, error) {
	bin := strings.TrimSpace(g.bin)
	if bin == "" {
		bin = "git"
	}

	cmdArgs := append([]string{"-C", repoDir}, args...)
	return g.runArgs(ctx, bin, cmdArgs...)
}

func (g *GitCommitter) runGlobal(ctx context.Context, args ...string) (string, error) {
	bin := strings.TrimSpace(g.bin)
	if bin == "" {
		bin = "git"
	}
	return g.runArgs(ctx, bin, args...)
}

func (g *GitCommitter) runArgs(ctx context.Context, bin string, cmdArgs ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"%s %q: %w (%s)",
			bin,
			strings.Join(cmdArgs, " "),
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return string(out), nil
}

func isGitConfigMissingValue(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}
