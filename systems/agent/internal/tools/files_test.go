package tools

import (
	"context"
	"strings"
	"testing"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

type stubFileExecutor struct {
	readPath        string
	readOffsetLines int
	readLimitLines  int
	writePath       string
	writeContent    string
	editPath        string
	editOldText     string
	editNewText     string
	patch           string
}

func (s *stubFileExecutor) ReadFile(
	_ context.Context,
	path string,
	offsetLines int,
	limitLines int,
) (sandboxcontract.ReadFileResult, error) {
	s.readPath = path
	s.readOffsetLines = offsetLines
	s.readLimitLines = limitLines
	return sandboxcontract.ReadFileResult{
		Content:         "alpha\nbeta",
		Truncated:       true,
		NextOffsetLines: 3,
		TotalLines:      5,
	}, nil
}

func (s *stubFileExecutor) WriteFile(
	_ context.Context,
	path string,
	content string,
) (sandboxcontract.WriteFileResult, error) {
	s.writePath = path
	s.writeContent = content
	return sandboxcontract.WriteFileResult{
		Path:         "/workspace/notes/today.md",
		BytesWritten: len(content),
	}, nil
}

func (s *stubFileExecutor) EditFile(
	_ context.Context,
	path string,
	oldText string,
	newText string,
) (sandboxcontract.EditFileResult, error) {
	s.editPath = path
	s.editOldText = oldText
	s.editNewText = newText
	return sandboxcontract.EditFileResult{
		Path:             "/workspace/main.go",
		Diff:             "-old\n+new",
		FirstChangedLine: 12,
	}, nil
}

func (s *stubFileExecutor) ApplyPatch(
	_ context.Context,
	patch string,
) (sandboxcontract.ApplyPatchResult, error) {
	s.patch = patch
	return sandboxcontract.ApplyPatchResult{
		ChangedFiles: []string{"/workspace/a.txt", "/workspace/b.txt"},
		Diff:         "=== /workspace/a.txt ===",
		Summary:      "applied patch to 2 file(s)",
	}, nil
}

func TestReadFileDefinitionAndRun(t *testing.T) {
	t.Parallel()

	exec := &stubFileExecutor{}
	tool := NewReadFile(exec)

	if got, want := tool.Definition().Name, "read_file"; got != want {
		t.Fatalf("Definition().Name = %q, want %q", got, want)
	}

	got, err := tool.Run(
		context.Background(),
		`{"path":"docs/readme.md","offset_lines":2,"limit_lines":40}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exec.readPath != "docs/readme.md" || exec.readOffsetLines != 2 || exec.readLimitLines != 40 {
		t.Fatalf("executor args = %+v, want path/offset/limit set", exec)
	}
	for _, want := range []string{
		"Path: docs/readme.md",
		"Total-Lines: 5",
		"Truncated: true",
		"Next-Offset-Lines: 3",
		"--- CONTENT ---",
		"alpha\nbeta",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, got)
		}
	}
}

func TestReadFileRunErrorsOnInvalidArguments(t *testing.T) {
	t.Parallel()

	tool := NewReadFile(&stubFileExecutor{})

	if _, err := tool.Run(context.Background(), "{"); err == nil ||
		!strings.Contains(err.Error(), "invalid arguments JSON") {
		t.Fatalf("Run() error = %v, want invalid JSON error", err)
	}
	if _, err := tool.Run(context.Background(), `{"path":"   "}`); err == nil ||
		!strings.Contains(err.Error(), "missing required argument: path") {
		t.Fatalf("Run() error = %v, want missing path error", err)
	}
	if _, err := tool.Run(context.Background(), `{"path":"main.go","offset_lines":-1}`); err == nil ||
		!strings.Contains(err.Error(), "offset_lines must be >= 0") {
		t.Fatalf("Run() error = %v, want offset validation error", err)
	}
}

func TestWriteFileDefinitionAndRun(t *testing.T) {
	t.Parallel()

	exec := &stubFileExecutor{}
	tool := NewWriteFile(exec)

	if got, want := tool.Definition().Name, "write_file"; got != want {
		t.Fatalf("Definition().Name = %q, want %q", got, want)
	}

	got, err := tool.Run(context.Background(), `{"path":"notes/today.md","content":"hello"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exec.writePath != "notes/today.md" || exec.writeContent != "hello" {
		t.Fatalf("executor args = %+v, want path/content set", exec)
	}
	if got != "Path: /workspace/notes/today.md\nBytes-Written: 5" {
		t.Fatalf("Run() = %q", got)
	}
}

func TestEditFileDefinitionAndRun(t *testing.T) {
	t.Parallel()

	exec := &stubFileExecutor{}
	tool := NewEditFile(exec)

	if got, want := tool.Definition().Name, "edit_file"; got != want {
		t.Fatalf("Definition().Name = %q, want %q", got, want)
	}

	got, err := tool.Run(
		context.Background(),
		`{"path":"main.go","old_text":"old","new_text":"new"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exec.editPath != "main.go" || exec.editOldText != "old" || exec.editNewText != "new" {
		t.Fatalf("executor args = %+v, want path/old/new set", exec)
	}
	for _, want := range []string{"Path: /workspace/main.go", "First-Changed-Line: 12", "--- DIFF ---", "-old\n+new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, got)
		}
	}
}

func TestEditFileRunErrorsOnMissingOldText(t *testing.T) {
	t.Parallel()

	tool := NewEditFile(&stubFileExecutor{})
	if _, err := tool.Run(context.Background(), `{"path":"main.go","old_text":"","new_text":"new"}`); err == nil ||
		!strings.Contains(err.Error(), "missing required argument: old_text") {
		t.Fatalf("Run() error = %v, want missing old_text error", err)
	}
}

func TestApplyPatchDefinitionAndRun(t *testing.T) {
	t.Parallel()

	exec := &stubFileExecutor{}
	tool := NewApplyPatch(exec)

	def := tool.Definition()
	if got, want := def.Name, "apply_patch"; got != want {
		t.Fatalf("Definition().Name = %q, want %q", got, want)
	}
	if !strings.Contains(def.Description, "not unified diff or git diff syntax") {
		t.Fatalf("Definition().Description = %q, want syntax guidance", def.Description)
	}
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf(
			"Definition().Parameters.properties has unexpected type %T",
			def.Parameters["properties"],
		)
	}
	patchProperty, ok := properties["patch"].(map[string]string)
	if !ok {
		t.Fatalf(
			"Definition().Parameters.properties.patch has unexpected type %T",
			properties["patch"],
		)
	}
	if !strings.Contains(patchProperty["description"], "*** Begin Patch") {
		t.Fatalf(
			"patch property description = %q, want explicit patch syntax",
			patchProperty["description"],
		)
	}

	got, err := tool.Run(context.Background(), `{"patch":"*** Begin Patch\n*** End Patch"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exec.patch != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("executor patch = %q", exec.patch)
	}
	for _, want := range []string{
		"Summary: applied patch to 2 file(s)",
		"Changed-Files:",
		"- /workspace/a.txt",
		"- /workspace/b.txt",
		"--- DIFF ---",
		"=== /workspace/a.txt ===",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, got)
		}
	}
}

func TestApplyPatchRunErrorsOnMissingPatch(t *testing.T) {
	t.Parallel()

	tool := NewApplyPatch(&stubFileExecutor{})
	if _, err := tool.Run(context.Background(), `{"patch":"   "}`); err == nil ||
		!strings.Contains(err.Error(), "missing required argument: patch") {
		t.Fatalf("Run() error = %v, want missing patch error", err)
	}
}
