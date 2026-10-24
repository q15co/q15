package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AcquireFileMutation holds the repository lease for an entire file-tool
// mutation. Begin and recovery take the same lease, eliminating the gap between
// checking ownership and changing bytes. Non-memory roots do not need a lease.
func (s *Store) AcquireFileMutation(ctx context.Context, localPaths []string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release := s.repository.Acquire()
	retainLease := false
	defer func() {
		if !retainLease {
			release()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(s.root())
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	var relatives []string
	for _, localPath := range localPaths {
		logical, err := filepath.Rel(s.root(), filepath.Clean(localPath))
		if err != nil {
			return nil, err
		}
		if logical != ".." && !strings.HasPrefix(logical, ".."+string(filepath.Separator)) {
			relatives = append(relatives, filepath.ToSlash(logical))
		}
		canonical, err := canonicalMutationPath(localPath)
		if err != nil {
			return nil, fmt.Errorf("resolve mutation path: %w", err)
		}
		relative, err := filepath.Rel(root, canonical)
		if err != nil {
			return nil, err
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		relatives = append(relatives, filepath.ToSlash(relative))
	}
	if len(relatives) == 0 {
		return func() {}, nil
	}
	for _, relative := range relatives {
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			return nil, fmt.Errorf("memory repository metadata is managed by the runtime")
		}
	}
	retainLease = true
	return release, nil
}

// Resolve parents while preserving the leaf that atomic writes replace. A new
// file may have more than one missing parent directory.
func canonicalMutationPath(file string) (string, error) {
	file = filepath.Clean(file)
	current := filepath.Dir(file)
	suffix := []string{filepath.Base(file)}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
