package sandboxfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

func TestApplyPatchSupportsAddUpdateDeleteAndMove(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "update.txt", "one\ntwo\n")
	writeHostFile(t, cfg.WorkspaceHostDir, "delete.txt", "bye\n")
	writeHostFile(t, cfg.WorkspaceHostDir, "move.txt", "move me\n")

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: added.txt",
		"+hello",
		"+world",
		"*** Update File: update.txt",
		"@@",
		" one",
		"-two",
		"+three",
		"*** Delete File: delete.txt",
		"*** Update File: move.txt",
		"*** Move to: moved.txt",
		"*** End Patch",
	}, "\n")

	got, err := ApplyPatch(cfg, sandboxcontract.ApplyPatchRequest{Patch: patch})
	if err != nil {
		t.Fatalf("ApplyPatch() error = %v", err)
	}

	if got.Summary != "applied patch to 5 file(s)" {
		t.Fatalf("Summary = %q", got.Summary)
	}
	for _, want := range []string{
		"/workspace/added.txt",
		"/workspace/delete.txt",
		"/workspace/move.txt",
		"/workspace/moved.txt",
		"/workspace/update.txt",
	} {
		if !containsString(got.ChangedFiles, want) {
			t.Fatalf("ChangedFiles missing %q: %v", want, got.ChangedFiles)
		}
	}
	for _, want := range []string{"=== /workspace/added.txt ===", "=== /workspace/update.txt ===", "rename /workspace/move.txt -> /workspace/moved.txt"} {
		if !strings.Contains(got.Diff, want) {
			t.Fatalf("Diff missing %q:\n%s", want, got.Diff)
		}
	}

	assertHostFileContent(t, cfg.WorkspaceHostDir, "added.txt", "hello\nworld")
	assertHostFileContent(t, cfg.WorkspaceHostDir, "update.txt", "one\nthree\n")
	assertHostFileMissing(t, cfg.WorkspaceHostDir, "delete.txt")
	assertHostFileMissing(t, cfg.WorkspaceHostDir, "move.txt")
	assertHostFileContent(t, cfg.WorkspaceHostDir, "moved.txt", "move me\n")
}

func TestApplyPatchRejectsMalformedPatch(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	_, err := ApplyPatch(cfg, sandboxcontract.ApplyPatchRequest{Patch: "*** Update File: bad.txt"})
	if err == nil || !strings.Contains(err.Error(), "must begin") {
		t.Fatalf("ApplyPatch() error = %v, want malformed patch rejection", err)
	}
}

func TestApplyPatchRejectsAmbiguousHunks(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "dup.txt", "a\nx\nb\na\nx\nb\n")

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: dup.txt",
		"@@",
		" a",
		" x",
		" b",
		"+c",
		"*** End Patch",
	}, "\n")

	_, err := ApplyPatch(cfg, sandboxcontract.ApplyPatchRequest{Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "matched multiple locations") {
		t.Fatalf("ApplyPatch() error = %v, want ambiguous hunk rejection", err)
	}
}

func TestApplyPatchRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: /etc/passwd",
		"+bad",
		"*** End Patch",
	}, "\n")

	_, err := ApplyPatch(cfg, sandboxcontract.ApplyPatchRequest{Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "absolute paths must be under") {
		t.Fatalf("ApplyPatch() error = %v, want invalid path rejection", err)
	}
}

func TestApplyPatchValidatesEntirePatchBeforeMutation(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: good.txt",
		"+hello",
		"*** Delete File: missing.txt",
		"*** End Patch",
	}, "\n")

	_, err := ApplyPatch(cfg, sandboxcontract.ApplyPatchRequest{Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "cannot delete missing file") {
		t.Fatalf("ApplyPatch() error = %v, want missing file rejection", err)
	}

	assertHostFileMissing(t, cfg.WorkspaceHostDir, "good.txt")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertHostFileContent(t *testing.T, root, rel, want string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", rel, err)
	}
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", rel, string(raw), want)
	}
}

func assertHostFileMissing(t *testing.T, root, rel string) {
	t.Helper()

	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not-exist", rel, err)
	}
}
