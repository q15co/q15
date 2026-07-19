package memoryrepo

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

// GitCommitter records memory changes in a Git repository.
type GitCommitter struct {
	bin string
}

// NewGitCommitter constructs a Git-backed state committer.
func NewGitCommitter() *GitCommitter {
	return &GitCommitter{bin: "git"}
}

// EnsureRepo initializes and configures the memory repository when needed.
func (g *GitCommitter) EnsureRepo(ctx context.Context, repoDir string) error {
	if g == nil {
		return fmt.Errorf("nil Git committer")
	}
	if err := g.ensureSafeDirectory(ctx, repoDir); err != nil {
		return err
	}

	_, err := g.run(ctx, repoDir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if _, initErr := g.run(ctx, repoDir, "init"); initErr != nil {
			return fmt.Errorf("initialize memory repository: %w", initErr)
		}
	}

	if _, err := g.run(ctx, repoDir, "config", "user.name", defaultGitUserName); err != nil {
		return fmt.Errorf("configure Git user.name: %w", err)
	}
	if _, err := g.run(ctx, repoDir, "config", "user.email", defaultGitUserEmail); err != nil {
		return fmt.Errorf("configure Git user.email: %w", err)
	}
	if _, err := g.run(ctx, repoDir, "config", "commit.gpgsign", "false"); err != nil {
		return fmt.Errorf("configure Git commit.gpgsign: %w", err)
	}

	return nil
}

func (g *GitCommitter) ensureSafeDirectory(ctx context.Context, repoDir string) error {
	repoDir = filepath.Clean(strings.TrimSpace(repoDir))
	if repoDir == "" {
		return fmt.Errorf("memory repository dir is required")
	}

	out, err := g.runGlobal(ctx, "config", "--global", "--get-all", "safe.directory")
	if err != nil && !isGitConfigMissingValue(err) {
		return fmt.Errorf("read Git safe.directory: %w", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if filepath.Clean(strings.TrimSpace(line)) == repoDir {
			return nil
		}
	}

	if _, err := g.runGlobal(ctx, "config", "--global", "--add", "safe.directory", repoDir); err != nil {
		return fmt.Errorf("configure Git safe.directory: %w", err)
	}
	return nil
}

// CommitPaths stages and commits only paths. Unrelated staged and unstaged
// changes are left untouched.
func (g *GitCommitter) CommitPaths(
	ctx context.Context,
	repoDir string,
	message string,
	paths []string,
) error {
	if g == nil {
		return fmt.Errorf("nil Git committer")
	}

	pathArgs := append([]string{"add", "-A", "--"}, paths...)
	if _, err := g.run(ctx, repoDir, pathArgs...); err != nil {
		return fmt.Errorf("git add memory changes: %w", err)
	}

	statusArgs := append([]string{"status", "--porcelain", "--"}, paths...)
	statusOut, err := g.run(ctx, repoDir, statusArgs...)
	if err != nil {
		return fmt.Errorf("git status memory changes: %w", err)
	}
	if strings.TrimSpace(statusOut) == "" {
		return nil
	}

	commitArgs := []string{"commit", "--only", "--no-gpg-sign", "-m", message, "--"}
	commitArgs = append(commitArgs, paths...)
	if _, err := g.run(ctx, repoDir, commitArgs...); err != nil {
		return fmt.Errorf("git commit state changes: %w", err)
	}
	return nil
}

// ResetPaths restores paths in the index to HEAD without changing the
// worktree. In an unborn repository, it removes newly staged entries instead.
func (g *GitCommitter) ResetPaths(
	ctx context.Context,
	repoDir string,
	paths []string,
) error {
	if g == nil {
		return fmt.Errorf("nil Git committer")
	}

	if _, err := g.run(ctx, repoDir, "rev-parse", "--verify", "HEAD"); err == nil {
		args := append([]string{"reset", "--quiet", "HEAD", "--"}, paths...)
		if _, err := g.run(ctx, repoDir, args...); err != nil {
			return fmt.Errorf("reset Git memory paths: %w", err)
		}
		return nil
	}

	args := append([]string{"rm", "--cached", "-r", "--ignore-unmatch", "--"}, paths...)
	if _, err := g.run(ctx, repoDir, args...); err != nil {
		return fmt.Errorf("reset unborn Git memory paths: %w", err)
	}
	return nil
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
