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
	owner := cognition.MemoryRecoveryOwner(ctx)
	for _, relative := range relatives {
		if frameworkWorkingPath(relative) && (!exists || owner == "" || owner != journal.ID) {
			release()
			return nil, fmt.Errorf("working-memory path %s is managed by cognition jobs", relative)
		}
	}
	if exists && owner != journal.ID {
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

func frameworkWorkingPath(relative string) bool {
	switch relative {
	case workingDirPath + "/" + workingMemoryFileName,
		workingDirPath + "/" + openLoopsFileName,
		workingLedgerRelativePath:
		return true
	default:
		return false
	}
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
