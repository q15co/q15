package fileops

import "context"

// Executor adapts rooted file operations to the tool-facing interface shape.
type Executor struct {
	cfg Settings
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
	_ context.Context,
	path string,
	content string,
) (WriteResult, error) {
	return WriteFile(e.cfg, path, content)
}

// EditFile performs one exact text replacement in one configured root.
func (e *Executor) EditFile(
	_ context.Context,
	path string,
	oldText string,
	newText string,
) (EditResult, error) {
	return EditFile(e.cfg, path, oldText, newText)
}

// ApplyPatch applies a multi-file patch in the configured roots.
func (e *Executor) ApplyPatch(_ context.Context, patch string) (ApplyPatchResult, error) {
	return ApplyPatch(e.cfg, patch)
}
