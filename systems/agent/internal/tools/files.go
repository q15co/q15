// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

// FileToolExecutor performs helper-backed file operations inside the sandbox roots.
type FileToolExecutor interface {
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

// ReadFile reads a UTF-8 text file from /workspace, /memory, or /skills.
type ReadFile struct {
	exec FileToolExecutor
}

// NewReadFile constructs a read_file tool backed by the provided executor.
func NewReadFile(exec FileToolExecutor) *ReadFile {
	return &ReadFile{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (r *ReadFile) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the sandbox workspace, memory, or skills roots with optional line-based pagination",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type": "string",
				},
				"offset_lines": map[string]any{
					"type":    "integer",
					"minimum": 0,
				},
				"limit_lines": map[string]any{
					"type":    "integer",
					"minimum": 0,
				},
			},
			"required": []string{"path"},
		},
	}
}

// Run executes one read_file tool call from raw JSON arguments.
func (r *ReadFile) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Path        string `json:"path"`
		OffsetLines int    `json:"offset_lines"`
		LimitLines  int    `json:"limit_lines"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	path, err := normalizeToolPath(args.Path)
	if err != nil {
		return "", err
	}
	if args.OffsetLines < 0 {
		return "", fmt.Errorf("offset_lines must be >= 0")
	}
	if args.LimitLines < 0 {
		return "", fmt.Errorf("limit_lines must be >= 0")
	}
	if r.exec == nil {
		return "", fmt.Errorf("no file executor configured")
	}

	result, err := r.exec.ReadFile(ctx, path, args.OffsetLines, args.LimitLines)
	if err != nil {
		return "", err
	}

	var lines []string
	lines = append(lines, "Path: "+path)
	lines = append(lines, fmt.Sprintf("Total-Lines: %d", result.TotalLines))
	lines = append(lines, fmt.Sprintf("Truncated: %t", result.Truncated))
	if result.Truncated && result.NextOffsetLines > 0 {
		lines = append(lines, fmt.Sprintf("Next-Offset-Lines: %d", result.NextOffsetLines))
	}
	lines = append(lines, "--- CONTENT ---")
	lines = append(lines, result.Content)
	return strings.Join(lines, "\n"), nil
}

// WriteFile creates or replaces a UTF-8 text file in /workspace, /memory, or /skills.
type WriteFile struct {
	exec FileToolExecutor
}

// NewWriteFile constructs a write_file tool backed by the provided executor.
func NewWriteFile(exec FileToolExecutor) *WriteFile {
	return &WriteFile{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (w *WriteFile) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "write_file",
		Description: "Create or replace a UTF-8 text file in the sandbox workspace, memory, or shared skills roots",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type": "string",
				},
				"content": map[string]string{
					"type": "string",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

// Run executes one write_file tool call from raw JSON arguments.
func (w *WriteFile) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	path, err := normalizeToolPath(args.Path)
	if err != nil {
		return "", err
	}
	if w.exec == nil {
		return "", fmt.Errorf("no file executor configured")
	}

	result, err := w.exec.WriteFile(ctx, path, args.Content)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Path: %s\nBytes-Written: %d", result.Path, result.BytesWritten), nil
}

// EditFile performs one exact text replacement in an existing UTF-8 text file.
type EditFile struct {
	exec FileToolExecutor
}

// NewEditFile constructs an edit_file tool backed by the provided executor.
func NewEditFile(exec FileToolExecutor) *EditFile {
	return &EditFile{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (e *EditFile) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "edit_file",
		Description: "Perform one exact text replacement in an existing UTF-8 text file in the sandbox workspace, memory, or shared skills roots",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type": "string",
				},
				"old_text": map[string]string{
					"type": "string",
				},
				"new_text": map[string]string{
					"type": "string",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

// Run executes one edit_file tool call from raw JSON arguments.
func (e *EditFile) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	path, err := normalizeToolPath(args.Path)
	if err != nil {
		return "", err
	}
	if args.OldText == "" {
		return "", fmt.Errorf("missing required argument: old_text")
	}
	if e.exec == nil {
		return "", fmt.Errorf("no file executor configured")
	}

	result, err := e.exec.EditFile(ctx, path, args.OldText, args.NewText)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Path: %s\nFirst-Changed-Line: %d\n--- DIFF ---\n%s",
		result.Path,
		result.FirstChangedLine,
		result.Diff,
	), nil
}

// ApplyPatch applies a multi-file high-level patch inside the sandbox roots.
type ApplyPatch struct {
	exec FileToolExecutor
}

// NewApplyPatch constructs an apply_patch tool backed by the provided executor.
func NewApplyPatch(exec FileToolExecutor) *ApplyPatch {
	return &ApplyPatch{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (a *ApplyPatch) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "apply_patch",
		Description: "Apply a multi-file high-level patch inside the sandbox workspace, memory, or shared skills roots using the Codex patch envelope, not unified diff or git diff syntax",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch": map[string]string{
					"type":        "string",
					"description": "Patch text using the Codex envelope: *** Begin Patch, then *** Add File:/*** Delete File:/*** Update File:, optional *** Move to:, @@ hunks with space/-/+, and *** End Patch. Do not use diff --git, ---, or +++ headers.",
				},
			},
			"required": []string{"patch"},
		},
	}
}

// Run executes one apply_patch tool call from raw JSON arguments.
func (a *ApplyPatch) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if strings.TrimSpace(args.Patch) == "" {
		return "", fmt.Errorf("missing required argument: patch")
	}
	if a.exec == nil {
		return "", fmt.Errorf("no file executor configured")
	}

	result, err := a.exec.ApplyPatch(ctx, args.Patch)
	if err != nil {
		return "", err
	}

	var lines []string
	lines = append(lines, "Summary: "+result.Summary)
	if len(result.ChangedFiles) > 0 {
		lines = append(lines, "Changed-Files:")
		for _, path := range result.ChangedFiles {
			lines = append(lines, "- "+path)
		}
	}
	lines = append(lines, "--- DIFF ---")
	lines = append(lines, result.Diff)
	return strings.Join(lines, "\n"), nil
}

func normalizeToolPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	return path, nil
}
