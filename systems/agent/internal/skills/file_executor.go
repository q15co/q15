// Package skills manages discovery, validation, and file access for q15 skills.
package skills

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/fileops"
)

// FileToolDelegate is the rooted file executor used for shared,
// workspace, and memory paths.
type FileToolDelegate interface {
	ReadFile(
		ctx context.Context,
		path string,
		offsetLines int,
		limitLines int,
	) (fileops.ReadResult, error)
	WriteFile(
		ctx context.Context,
		path string,
		content string,
	) (fileops.WriteResult, error)
	EditFile(
		ctx context.Context,
		path string,
		oldText string,
		newText string,
	) (fileops.EditResult, error)
	ApplyPatch(ctx context.Context, patch string) (fileops.ApplyPatchResult, error)
}

// FileExecutor adds builtin skill reads and shared-skills configuration checks
// on top of the rooted file executor.
type FileExecutor struct {
	delegate FileToolDelegate
	manager  *Manager
}

// NewFileExecutor constructs a skill-aware file executor.
func NewFileExecutor(delegate FileToolDelegate, manager *Manager) *FileExecutor {
	return &FileExecutor{
		delegate: delegate,
		manager:  manager,
	}
}

// ReadFile reads a text file from builtin, shared, or workspace-backed roots.
func (e *FileExecutor) ReadFile(
	ctx context.Context,
	rawPath string,
	offsetLines int,
	limitLines int,
) (fileops.ReadResult, error) {
	if e.manager != nil {
		if result, handled, err := e.manager.ReadBuiltinFile(rawPath, offsetLines, limitLines); handled {
			return result, err
		}
		if err := e.validateSharedFilesystemAccess(rawPath); err != nil {
			return fileops.ReadResult{}, err
		}
	}
	if e.delegate == nil {
		return fileops.ReadResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.ReadFile(ctx, rawPath, offsetLines, limitLines)
}

// WriteFile writes a UTF-8 text file to a writable skill or workspace path.
func (e *FileExecutor) WriteFile(
	ctx context.Context,
	rawPath string,
	content string,
) (fileops.WriteResult, error) {
	if err := e.ensureWritablePath(rawPath); err != nil {
		return fileops.WriteResult{}, err
	}
	if e.delegate == nil {
		return fileops.WriteResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.WriteFile(ctx, rawPath, content)
}

// EditFile applies one exact replacement to a writable skill or workspace path.
func (e *FileExecutor) EditFile(
	ctx context.Context,
	rawPath string,
	oldText string,
	newText string,
) (fileops.EditResult, error) {
	if err := e.ensureWritablePath(rawPath); err != nil {
		return fileops.EditResult{}, err
	}
	if e.delegate == nil {
		return fileops.EditResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.EditFile(ctx, rawPath, oldText, newText)
}

// ApplyPatch applies a multi-file patch while protecting builtin skill paths.
func (e *FileExecutor) ApplyPatch(
	ctx context.Context,
	patch string,
) (fileops.ApplyPatchResult, error) {
	if e.manager != nil {
		root := path.Join(e.manager.SkillsDir(), BuiltinNamespace)
		if strings.Contains(patch, root) {
			return fileops.ApplyPatchResult{}, fmt.Errorf(
				"builtin skills under %s are read-only",
				root,
			)
		}
		if strings.Contains(patch, e.manager.SkillsDir()+"/") && !e.manager.SharedSkillsEnabled() {
			return fileops.ApplyPatchResult{}, fmt.Errorf(
				"shared skills root is not configured",
			)
		}
	}
	if e.delegate == nil {
		return fileops.ApplyPatchResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.ApplyPatch(ctx, patch)
}

func (e *FileExecutor) ensureWritablePath(rawPath string) error {
	if e.manager == nil {
		return nil
	}
	cleaned := path.Clean(strings.TrimSpace(rawPath))
	root := path.Join(e.manager.SkillsDir(), BuiltinNamespace)
	switch {
	case cleaned == root || strings.HasPrefix(cleaned, root+"/"):
		return fmt.Errorf("builtin skills under %s are read-only", root)
	case strings.HasPrefix(cleaned, e.manager.SkillsDir()+"/") && !e.manager.SharedSkillsEnabled():
		return fmt.Errorf("shared skills root is not configured")
	default:
		return nil
	}
}

func (e *FileExecutor) validateSharedFilesystemAccess(rawPath string) error {
	if e.manager == nil {
		return nil
	}
	cleaned := path.Clean(strings.TrimSpace(rawPath))
	if strings.HasPrefix(cleaned, e.manager.SkillsDir()+"/") &&
		!strings.HasPrefix(cleaned, path.Join(e.manager.SkillsDir(), BuiltinNamespace)+"/") &&
		!e.manager.SharedSkillsEnabled() {
		return fmt.Errorf("shared skills root is not configured")
	}
	return nil
}
