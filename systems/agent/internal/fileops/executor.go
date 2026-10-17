package fileops

import (
	"context"
	"path/filepath"
)

// Executor adapts rooted file operations to the tool-facing interface shape.
type Executor struct {
	cfg   Settings
	guard MutationGuard
}

// MutationGuard serializes actual filesystem writes against memory recovery.
type MutationGuard interface {
	AcquireFileMutation(ctx context.Context, localPaths []string) (func(), error)
}

// NewGuardedExecutor constructs an executor whose writes share a mutation lease.
func NewGuardedExecutor(cfg Settings, guard MutationGuard) *Executor {
	return &Executor{cfg: cfg, guard: guard}
}

// NewExecutor constructs an executor for one rooted filesystem mapping.
func NewExecutor(cfg Settings) *Executor {
	return &Executor{cfg: cfg}
}

// ReadFile reads a UTF-8 text file from one configured root.
func (e *Executor) ReadFile(
	_ context.Context,
	path string,
	offsetLines int,
	limitLines int,
) (ReadResult, error) {
	return ReadFile(e.cfg, path, offsetLines, limitLines)
}

// WriteFile creates or replaces a UTF-8 text file under one configured root.
func (e *Executor) WriteFile(
	ctx context.Context,
	path string,
	content string,
) (WriteResult, error) {
	release, err := e.acquireMutation(ctx, []string{path})
	if err != nil {
		return WriteResult{}, err
	}
	defer release()
	return WriteFile(e.cfg, path, content)
}

// EditFile performs one exact text replacement in one configured root.
func (e *Executor) EditFile(
	ctx context.Context,
	path string,
	oldText string,
	newText string,
) (EditResult, error) {
	release, err := e.acquireMutation(ctx, []string{path})
	if err != nil {
		return EditResult{}, err
	}
	defer release()
	return EditFile(e.cfg, path, oldText, newText)
}

// ApplyPatch applies a multi-file patch in the configured roots.
func (e *Executor) ApplyPatch(ctx context.Context, patch string) (ApplyPatchResult, error) {
	ops, err := parsePatch(patch)
	if err != nil {
		return ApplyPatchResult{}, err
	}
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.path)
		if op.moveTo != "" {
			paths = append(paths, op.moveTo)
		}
	}
	release, err := e.acquireMutation(ctx, paths)
	if err != nil {
		return ApplyPatchResult{}, err
	}
	defer release()
	return ApplyPatch(e.cfg, patch)
}

func (e *Executor) acquireMutation(ctx context.Context, paths []string) (func(), error) {
	if e.guard == nil {
		return func() {}, nil
	}
	localPaths := make([]string, 0, len(paths))
	for _, rawPath := range paths {
		resolved, err := resolvePath(e.cfg, rawPath)
		if err != nil {
			return nil, err
		}
		localPaths = append(localPaths, filepath.Join(resolved.rootLocalDir, resolved.rel))
	}
	return e.guard.AcquireFileMutation(ctx, localPaths)
}
