package sandboxfiles

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

func TestReadFileSupportsPaginationAndByteTruncation(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "notes.txt", "one\ntwo\nthree\n")

	got, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{
		Path:        "notes.txt",
		OffsetLines: 2,
		LimitLines:  2,
	})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got.Content != "two\nthree" {
		t.Fatalf("Content = %q, want %q", got.Content, "two\nthree")
	}
	if got.Truncated {
		t.Fatalf("Truncated = true, want false")
	}
	if got.TotalLines != 3 {
		t.Fatalf("TotalLines = %d, want 3", got.TotalLines)
	}

	longLine := strings.Repeat("a", 9000)
	writeHostFile(t, cfg.WorkspaceHostDir, "wide.txt", longLine+"\n"+longLine+"\n")

	truncated, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{Path: "wide.txt"})
	if err != nil {
		t.Fatalf("ReadFile() on wide file error = %v", err)
	}
	if truncated.Content != longLine {
		t.Fatalf("wide Content length = %d, want %d", len(truncated.Content), len(longLine))
	}
	if !truncated.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if truncated.NextOffsetLines != 2 {
		t.Fatalf("NextOffsetLines = %d, want 2", truncated.NextOffsetLines)
	}
	if truncated.TotalLines != 2 {
		t.Fatalf("TotalLines = %d, want 2", truncated.TotalLines)
	}
}

func TestReadFileRejectsInvalidOffsetsAndNonText(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "notes.txt", "one\n")

	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{
		Path:        "notes.txt",
		OffsetLines: 0,
		LimitLines:  -1,
	}); err == nil || !strings.Contains(err.Error(), "limit_lines must be >= 1") {
		t.Fatalf("ReadFile() error = %v, want limit validation error", err)
	}

	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{
		Path:        "notes.txt",
		OffsetLines: 3,
	}); err == nil || !strings.Contains(err.Error(), "beyond end of file") {
		t.Fatalf("ReadFile() error = %v, want offset error", err)
	}

	writeHostFileBytes(t, cfg.WorkspaceHostDir, "nul.bin", []byte("a\x00b"))
	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{Path: "nul.bin"}); err == nil ||
		!strings.Contains(err.Error(), "NUL bytes") {
		t.Fatalf("ReadFile() error = %v, want NUL rejection", err)
	}

	writeHostFileBytes(t, cfg.WorkspaceHostDir, "invalid.bin", []byte{0xff, 0xfe})
	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{Path: "invalid.bin"}); err == nil ||
		!strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("ReadFile() error = %v, want UTF-8 rejection", err)
	}
}

func TestReadFileRejectsTraversalAndSymlinkEscape(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}
	linkPath := filepath.Join(cfg.WorkspaceHostDir, "link.txt")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{Path: "../secret.txt"}); err == nil ||
		(!strings.Contains(err.Error(), "escapes root") && !strings.Contains(err.Error(), "is invalid")) {
		t.Fatalf("ReadFile() error = %v, want traversal rejection", err)
	}

	if _, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{Path: "link.txt"}); err == nil {
		t.Fatal("ReadFile() unexpectedly succeeded through escaping symlink")
	}
}

func TestWriteFileCreatesParentsAndSupportsMemoryRoot(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)

	got, err := WriteFile(cfg, sandboxcontract.WriteFileRequest{
		Path:    "/memory/notes/today.md",
		Content: "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got.Path != "/memory/notes/today.md" {
		t.Fatalf("Path = %q, want %q", got.Path, "/memory/notes/today.md")
	}
	if got.BytesWritten != len("hello\nworld\n") {
		t.Fatalf("BytesWritten = %d", got.BytesWritten)
	}

	raw, err := os.ReadFile(filepath.Join(cfg.MemoryHostDir, "notes", "today.md"))
	if err != nil {
		t.Fatalf("ReadFile(memory host) error = %v", err)
	}
	if string(raw) != "hello\nworld\n" {
		t.Fatalf("host content = %q, want %q", string(raw), "hello\nworld\n")
	}
}

func TestSkillsRootSupportsReadWriteAndEdit(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.SkillsHostDir, "shared/SKILL.md", "one\ntwo\n")

	readResult, err := ReadFile(cfg, sandboxcontract.ReadFileRequest{
		Path: "/skills/shared/SKILL.md",
	})
	if err != nil {
		t.Fatalf("ReadFile(skills) error = %v", err)
	}
	if readResult.Content != "one\ntwo" {
		t.Fatalf("ReadFile(skills).Content = %q", readResult.Content)
	}

	writeResult, err := WriteFile(cfg, sandboxcontract.WriteFileRequest{
		Path:    "/skills/shared/references/info.md",
		Content: "hello\n",
	})
	if err != nil {
		t.Fatalf("WriteFile(skills) error = %v", err)
	}
	if writeResult.Path != "/skills/shared/references/info.md" {
		t.Fatalf("WriteFile(skills).Path = %q", writeResult.Path)
	}

	editResult, err := EditFile(cfg, sandboxcontract.EditFileRequest{
		Path:    "/skills/shared/SKILL.md",
		OldText: "two\n",
		NewText: "three\n",
	})
	if err != nil {
		t.Fatalf("EditFile(skills) error = %v", err)
	}
	if editResult.Path != "/skills/shared/SKILL.md" {
		t.Fatalf("EditFile(skills).Path = %q", editResult.Path)
	}
	assertHostFileContentFileOps(t, cfg.SkillsHostDir, "shared/SKILL.md", "one\nthree\n")
	assertHostFileContentFileOps(t, cfg.SkillsHostDir, "shared/references/info.md", "hello\n")
}

func TestEditFilePreservesBOMAndLineEndings(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	original := append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha\r\nbeta\r\n")...)
	writeHostFileBytes(t, cfg.WorkspaceHostDir, "doc.txt", original)

	got, err := EditFile(cfg, sandboxcontract.EditFileRequest{
		Path:    "doc.txt",
		OldText: "beta\n",
		NewText: "gamma\n",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if got.Path != "/workspace/doc.txt" {
		t.Fatalf("Path = %q, want %q", got.Path, "/workspace/doc.txt")
	}
	if got.FirstChangedLine != 2 {
		t.Fatalf("FirstChangedLine = %d, want 2", got.FirstChangedLine)
	}
	if !strings.Contains(got.Diff, "-beta") || !strings.Contains(got.Diff, "+gamma") {
		t.Fatalf("Diff = %q, want replacement excerpt", got.Diff)
	}

	raw, err := os.ReadFile(filepath.Join(cfg.WorkspaceHostDir, "doc.txt"))
	if err != nil {
		t.Fatalf("ReadFile(host) error = %v", err)
	}
	want := append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha\r\ngamma\r\n")...)
	if !bytes.Equal(raw, want) {
		t.Fatalf("edited bytes = %q, want %q", raw, want)
	}
}

func TestEditFileRejectsZeroAndMultipleMatches(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "dup.txt", "x\nx\n")

	if _, err := EditFile(cfg, sandboxcontract.EditFileRequest{
		Path:    "dup.txt",
		OldText: "x",
		NewText: "y",
	}); err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("EditFile() error = %v, want duplicate match error", err)
	}

	if _, err := EditFile(cfg, sandboxcontract.EditFileRequest{
		Path:    "dup.txt",
		OldText: "z",
		NewText: "y",
	}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("EditFile() error = %v, want not found error", err)
	}
}

func TestCommitDesiredStatesRollsBackOnFailure(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeHostFile(t, cfg.WorkspaceHostDir, "a.txt", "old\n")
	if err := os.Mkdir(filepath.Join(cfg.WorkspaceHostDir, "zdir"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	aPath, err := resolvePath(cfg, "a.txt")
	if err != nil {
		t.Fatalf("resolvePath(a.txt) error = %v", err)
	}
	zPath, err := resolvePath(cfg, "zdir")
	if err != nil {
		t.Fatalf("resolvePath(zdir) error = %v", err)
	}

	aSnapshot, err := snapshotPath(aPath)
	if err != nil {
		t.Fatalf("snapshotPath(a.txt) error = %v", err)
	}
	zSnapshot, err := snapshotPath(zPath)
	if err == nil {
		t.Fatalf("snapshotPath(zdir) unexpectedly succeeded")
	}
	_ = zSnapshot

	err = commitDesiredStates(
		map[string]desiredFileState{
			stateKey(aPath): {
				resolved:    aPath,
				shouldExist: true,
				raw:         []byte("new\n"),
			},
			stateKey(zPath): {
				resolved:    zPath,
				shouldExist: true,
				raw:         []byte("bad\n"),
			},
		},
		map[string]fileSnapshot{
			stateKey(aPath): aSnapshot,
			stateKey(zPath): {
				resolved: zPath,
				exists:   false,
			},
		},
	)
	if err == nil {
		t.Fatal("commitDesiredStates() unexpectedly succeeded")
	}

	raw, readErr := os.ReadFile(filepath.Join(cfg.WorkspaceHostDir, "a.txt"))
	if readErr != nil {
		t.Fatalf("ReadFile(a.txt) error = %v", readErr)
	}
	if string(raw) != "old\n" {
		t.Fatalf("a.txt after rollback = %q, want %q", string(raw), "old\n")
	}
}

func newTestSettings(t *testing.T) Settings {
	t.Helper()

	root := t.TempDir()
	workspaceHostDir := filepath.Join(root, "workspace")
	memoryHostDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(workspaceHostDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	if err := os.MkdirAll(memoryHostDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(memory) error = %v", err)
	}

	return Settings{
		WorkspaceHostDir: workspaceHostDir,
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    memoryHostDir,
		MemoryDir:        "/memory",
		SkillsHostDir:    filepath.Join(root, "skills"),
		SkillsDir:        "/skills",
	}
}

func writeHostFile(t *testing.T, root, rel, content string) {
	t.Helper()
	writeHostFileBytes(t, root, rel, []byte(content))
}

func writeHostFileBytes(t *testing.T, root, rel string, content []byte) {
	t.Helper()

	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", full, err)
	}
}

func assertHostFileContentFileOps(t *testing.T, root, rel, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", rel, err)
	}
	if string(raw) != want {
		t.Fatalf("%s content = %q, want %q", rel, string(raw), want)
	}
}
