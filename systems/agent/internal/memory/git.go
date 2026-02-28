package memory

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const (
	defaultGitUserName  = "q15-memory"
	defaultGitUserEmail = "q15@local"
)

type Committer interface {
	EnsureRepo(ctx context.Context, repoDir string) error
	CommitAll(ctx context.Context, repoDir, message string) (string, error)
}

type GitCommitter struct {
	bin string
}

func NewGitCommitter() *GitCommitter {
	return &GitCommitter{bin: "git"}
}

func (g *GitCommitter) EnsureRepo(ctx context.Context, repoDir string) error {
	if g == nil {
		return fmt.Errorf("nil git committer")
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
