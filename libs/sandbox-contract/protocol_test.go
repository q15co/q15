package sandboxcontract

import (
	"encoding/json"
	"testing"
)

func TestHelperRequestRoundTripsNewFilePayloads(t *testing.T) {
	req := HelperRequest{
		Settings: Settings{
			ContainerName:    "q15-test",
			WorkspaceHostDir: "/tmp/workspace",
			WorkspaceDir:     "/workspace",
			MemoryHostDir:    "/tmp/workspace/.q15-memory",
			MemoryDir:        "/memory",
			SkillsHostDir:    "/tmp/shared-skills",
			SkillsDir:        "/skills",
		},
		ReadFile: &ReadFileRequest{
			Path:        "/workspace/notes.txt",
			OffsetLines: 41,
			LimitLines:  25,
		},
		WriteFile: &WriteFileRequest{
			Path:    "/memory/log.txt",
			Content: "hello",
		},
		EditFile: &EditFileRequest{
			Path:    "README.md",
			OldText: "before",
			NewText: "after",
		},
		ApplyPatch: &ApplyPatchRequest{
			Patch: "*** Begin Patch\n*** End Patch\n",
		},
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got HelperRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.ReadFile == nil || got.ReadFile.Path != req.ReadFile.Path {
		t.Fatalf("ReadFile round-trip mismatch: %#v", got.ReadFile)
	}
	if got.WriteFile == nil || got.WriteFile.Content != req.WriteFile.Content {
		t.Fatalf("WriteFile round-trip mismatch: %#v", got.WriteFile)
	}
	if got.EditFile == nil || got.EditFile.NewText != req.EditFile.NewText {
		t.Fatalf("EditFile round-trip mismatch: %#v", got.EditFile)
	}
	if got.ApplyPatch == nil || got.ApplyPatch.Patch != req.ApplyPatch.Patch {
		t.Fatalf("ApplyPatch round-trip mismatch: %#v", got.ApplyPatch)
	}
	if got.Settings.SkillsHostDir != req.Settings.SkillsHostDir ||
		got.Settings.SkillsDir != req.Settings.SkillsDir {
		t.Fatalf("skills settings round-trip mismatch: %#v", got.Settings)
	}
}

func TestHelperResponseRoundTripsNewFileResults(t *testing.T) {
	resp := HelperResponse{
		ReadFile: &ReadFileResult{
			Content:         "chunk",
			Truncated:       true,
			NextOffsetLines: 401,
			TotalLines:      999,
		},
		WriteFile: &WriteFileResult{
			Path:         "/workspace/output.txt",
			BytesWritten: 5,
		},
		EditFile: &EditFileResult{
			Path:             "/workspace/app.txt",
			Diff:             "-old\n+new",
			FirstChangedLine: 12,
		},
		ApplyPatch: &ApplyPatchResult{
			ChangedFiles: []string{"/workspace/a.txt", "/memory/b.txt"},
			Diff:         "diff-body",
			Summary:      "patched 2 files",
		},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got HelperResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.ReadFile == nil || !got.ReadFile.Truncated {
		t.Fatalf("ReadFile round-trip mismatch: %#v", got.ReadFile)
	}
	if got.WriteFile == nil || got.WriteFile.BytesWritten != resp.WriteFile.BytesWritten {
		t.Fatalf("WriteFile round-trip mismatch: %#v", got.WriteFile)
	}
	if got.EditFile == nil || got.EditFile.FirstChangedLine != resp.EditFile.FirstChangedLine {
		t.Fatalf("EditFile round-trip mismatch: %#v", got.EditFile)
	}
	if got.ApplyPatch == nil || got.ApplyPatch.Summary != resp.ApplyPatch.Summary {
		t.Fatalf("ApplyPatch round-trip mismatch: %#v", got.ApplyPatch)
	}
}
