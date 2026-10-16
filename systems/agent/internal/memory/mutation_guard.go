package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// AcquireFileMutation holds the repository lease for an entire file-tool
// mutation. Begin and recovery take the same lease, eliminating the gap between
// checking ownership and changing bytes. Non-memory roots do not need a lease.
func (s *Store) AcquireFileMutation(ctx context.Context, localPaths []string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := canonicalMutationPath(s.root())
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	var relatives []string
	for _, localPath := range localPaths {
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
	release := s.repository.Acquire()
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	for _, relative := range relatives {
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			release()
			return nil, fmt.Errorf("memory repository metadata is managed by the runtime")
		}
	}
	journal, exists, err := s.loadMemoryRecoveryLocked()
	if err != nil {
		release()
		return nil, err
	}
	if exists && cognition.MemoryRecoveryOwner(ctx) != journal.ID {
		for _, relative := range relatives {
			if recoverableMemoryPath(relative, journal.TopicDir) {
				release()
				return nil, fmt.Errorf(
					"memory path %s is reserved by a cognition run; retry after recovery completes",
					relative,
				)
			}
		}
	}
	return release, nil
}

// Resolve existing ancestors so directory aliases cannot bypass the journal's
// path scope. A new file may have more than one missing parent directory.
func canonicalMutationPath(file string) (string, error) {
	current := filepath.Clean(file)
	var suffix []string
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
