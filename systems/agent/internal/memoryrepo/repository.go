// Package memoryrepo coordinates access to the agent's Git-backed memory
// repository.
package memoryrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Committer initializes and commits changes in a memory repository.
type Committer interface {
	EnsureRepo(ctx context.Context, repoDir string) error
	CommitPaths(ctx context.Context, repoDir, message string, paths []string) error
	ResetPaths(ctx context.Context, repoDir string, paths []string) error
}

// Repository owns the memory root path, synchronization, and Git commit
// boundary.
type Repository struct {
	mu        sync.Mutex
	root      string
	committer Committer
}

// New constructs a Git-backed memory repository rooted at root.
func New(root string, committer Committer) *Repository {
	if committer == nil {
		committer = NewGitCommitter()
	}
	root = strings.TrimSpace(root)
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Repository{
		root:      root,
		committer: committer,
	}
}

// Root returns the immutable repository root.
func (r *Repository) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// Init creates and initializes the repository root. It is safe to call more
// than once.
func (r *Repository) Init(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("memory repository is required")
	}

	release := r.Acquire()
	defer release()

	if strings.TrimSpace(r.root) == "" {
		return fmt.Errorf("memory repository root is required")
	}
	if !filepath.IsAbs(r.root) {
		return fmt.Errorf("memory repository root must be absolute")
	}
	if err := os.MkdirAll(r.root, 0o755); err != nil {
		return fmt.Errorf("create memory repository root %q: %w", r.root, err)
	}
	if err := r.committer.EnsureRepo(ctx, r.root); err != nil {
		return fmt.Errorf("ensure Git memory repository: %w", err)
	}
	return nil
}

// Acquire serializes one repository operation. The returned release function
// must be called before the operation returns.
func (r *Repository) Acquire() func() {
	r.mu.Lock()
	return r.mu.Unlock
}

// Commit records only the declared repository-relative paths. Callers must
// hold the repository lease returned by Acquire.
func (r *Repository) Commit(ctx context.Context, message string, paths ...string) error {
	if r == nil {
		return fmt.Errorf("memory repository is required")
	}
	normalized, err := normalizePaths(paths)
	if err != nil {
		return err
	}
	return r.committer.CommitPaths(ctx, r.root, message, normalized)
}

// ResetIndex restores the declared paths in the Git index to HEAD without
// changing the worktree. Callers must hold the repository lease returned by
// Acquire.
func (r *Repository) ResetIndex(ctx context.Context, paths ...string) error {
	if r == nil {
		return fmt.Errorf("memory repository is required")
	}
	normalized, err := normalizePaths(paths)
	if err != nil {
		return err
	}
	return r.committer.ResetPaths(ctx, r.root, normalized)
}

func normalizePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one memory repository path is required")
	}

	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || filepath.IsAbs(path) {
			return nil, fmt.Errorf("memory repository path must be relative: %q", path)
		}
		path = filepath.Clean(filepath.FromSlash(path))
		if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("memory repository path must stay under root: %q", path)
		}
		path = filepath.ToSlash(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized, nil
}
