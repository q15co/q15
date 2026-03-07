// Package skills manages discovery, validation, and file access for q15 skills.
package skills

import (
	"context"
	"fmt"
	"path"
	"strings"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

// FileToolDelegate is the helper-backed file executor used for shared,
// workspace, and memory paths.
type FileToolDelegate interface {
	ReadFile(
		ctx context.Context,
		path string,
		offsetLines int,
		limitLines int,
	) (sandboxcontract.ReadFileResult, error)
	WriteFile(
		ctx context.Context,
		path string,
		content string,
	) (sandboxcontract.WriteFileResult, error)
	EditFile(
		ctx context.Context,
		path string,
		oldText string,
		newText string,
	) (sandboxcontract.EditFileResult, error)
	ApplyPatch(ctx context.Context, patch string) (sandboxcontract.ApplyPatchResult, error)
}

// FileExecutor adds builtin skill reads and shared-skills configuration checks
// on top of the helper-backed file executor.
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
) (sandboxcontract.ReadFileResult, error) {
	if e.manager != nil {
		if result, handled, err := e.manager.ReadBuiltinFile(rawPath, offsetLines, limitLines); handled {
			return result, err
		}
		if err := e.validateSharedFilesystemAccess(rawPath); err != nil {
			return sandboxcontract.ReadFileResult{}, err
		}
	}
	if e.delegate == nil {
		return sandboxcontract.ReadFileResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.ReadFile(ctx, rawPath, offsetLines, limitLines)
}

// WriteFile writes a UTF-8 text file to a writable skill or workspace path.
func (e *FileExecutor) WriteFile(
	ctx context.Context,
	rawPath string,
	content string,
) (sandboxcontract.WriteFileResult, error) {
	if err := e.ensureWritablePath(rawPath); err != nil {
		return sandboxcontract.WriteFileResult{}, err
	}
	if e.delegate == nil {
		return sandboxcontract.WriteFileResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.WriteFile(ctx, rawPath, content)
}

// EditFile applies one exact replacement to a writable skill or workspace path.
func (e *FileExecutor) EditFile(
	ctx context.Context,
	rawPath string,
	oldText string,
	newText string,
) (sandboxcontract.EditFileResult, error) {
	if err := e.ensureWritablePath(rawPath); err != nil {
		return sandboxcontract.EditFileResult{}, err
	}
	if e.delegate == nil {
		return sandboxcontract.EditFileResult{}, fmt.Errorf("no file executor configured")
	}
	return e.delegate.EditFile(ctx, rawPath, oldText, newText)
}

// ApplyPatch applies a multi-file patch while protecting builtin skill paths.
func (e *FileExecutor) ApplyPatch(
	ctx context.Context,
	patch string,
) (sandboxcontract.ApplyPatchResult, error) {
	if e.manager != nil {
		root := path.Join(e.manager.SkillsDir(), BuiltinNamespace)
		if strings.Contains(patch, root) {
			return sandboxcontract.ApplyPatchResult{}, fmt.Errorf(
				"builtin skills under %s are read-only",
				root,
			)
		}
		if strings.Contains(patch, e.manager.SkillsDir()+"/") && !e.manager.SharedSkillsEnabled() {
			return sandboxcontract.ApplyPatchResult{}, fmt.Errorf(
				"shared skills root is not configured",
			)
		}
	}
	if e.delegate == nil {
		return sandboxcontract.ApplyPatchResult{}, fmt.Errorf("no file executor configured")
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
