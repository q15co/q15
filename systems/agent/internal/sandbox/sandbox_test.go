package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

func TestHelperCommandInvokesHelperDirectly(t *testing.T) {
	t.Parallel()

	cmd := helperCommand(context.Background(), "/tmp/q15-sandbox-helper", "prepare")

	if got, want := cmd.Path, "/tmp/q15-sandbox-helper"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if got, want := cmd.Args, []string{"/tmp/q15-sandbox-helper", "prepare"}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("cmd.Args = %v, want %v", got, want)
	}
}

func TestSettingsValidateRequiresAbsolutePaths(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		SkillsHostDir:    "/tmp/q15-skills",
		SkillsDir:        "/skills",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	cfg.WorkspaceHostDir = "relative"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for relative workspace host dir")
	}
}

func TestToContractSettingsMapsCoreFields(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		SkillsHostDir:    "/tmp/q15-skills",
		SkillsDir:        "/skills",
		Proxy: &ProxySettings{
			Enabled:      true,
			HTTPProxyURL: "http://127.0.0.1:18080",
		},
	}

	got := toContractSettings(cfg)
	if got.ContainerName != cfg.ContainerName {
		t.Fatalf("unexpected container name in contract settings: %q", got.ContainerName)
	}
	if got.Proxy == nil {
		t.Fatalf("expected proxy settings in contract payload")
	}
	if got.Proxy.HTTPProxyURL != cfg.Proxy.HTTPProxyURL {
		t.Fatalf("unexpected proxy URL in contract settings: %q", got.Proxy.HTTPProxyURL)
	}
	if got.SkillsHostDir != cfg.SkillsHostDir || got.SkillsDir != cfg.SkillsDir {
		t.Fatalf("unexpected skills settings in contract payload: %#v", got)
	}
}

func TestNewExecNixShellBashRequestNormalizesValues(t *testing.T) {
	req, err := newExecNixShellBashRequest(
		"  git --version  ",
		[]string{" nixpkgs#git ", "nixpkgs#jq"},
	)
	if err != nil {
		t.Fatalf("newExecNixShellBashRequest() error = %v", err)
	}
	if got, want := req.Command, "git --version"; got != want {
		t.Fatalf("Command = %q, want %q", got, want)
	}
	if got, want := strings.Join(req.Packages, ","), "nixpkgs#git,nixpkgs#jq"; got != want {
		t.Fatalf("Packages = %q, want %q", got, want)
	}
}

func TestNewExecNixShellBashHelperRequestIncludesStructuredPayload(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		SkillsHostDir:    "/tmp/q15-skills",
		SkillsDir:        "/skills",
	}
	req := newExecNixShellBashHelperRequest(
		cfg,
		ExecNixShellBashRequest{
			Command:  "git --version",
			Packages: []string{"nixpkgs#git"},
		},
	)

	if req.Command != "" {
		t.Fatalf("raw Command should be empty, got %q", req.Command)
	}
	if req.ExecNixShellBash == nil {
		t.Fatal("expected ExecNixShellBash payload")
	}
	if got, want := req.ExecNixShellBash.Command, "git --version"; got != want {
		t.Fatalf("ExecNixShellBash.Command = %q, want %q", got, want)
	}
	if got, want := strings.Join(req.ExecNixShellBash.Packages, ","), "nixpkgs#git"; got != want {
		t.Fatalf("ExecNixShellBash.Packages = %q, want %q", got, want)
	}
	if req.Settings.ContainerName != cfg.ContainerName {
		t.Fatalf(
			"Settings.ContainerName = %q, want %q",
			req.Settings.ContainerName,
			cfg.ContainerName,
		)
	}
}

func TestNewFileHelperRequestsIncludeStructuredPayloads(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
	}

	readReq := newReadFileHelperRequest(cfg, newReadFilePayload("docs/readme.md", 2, 40))
	if readReq.ReadFile == nil {
		t.Fatal("expected ReadFile payload")
	}
	if got, want := readReq.ReadFile.Path, "docs/readme.md"; got != want {
		t.Fatalf("ReadFile.Path = %q, want %q", got, want)
	}
	if got, want := readReq.ReadFile.OffsetLines, 2; got != want {
		t.Fatalf("ReadFile.OffsetLines = %d, want %d", got, want)
	}
	if got, want := readReq.ReadFile.LimitLines, 40; got != want {
		t.Fatalf("ReadFile.LimitLines = %d, want %d", got, want)
	}

	writeReq := newWriteFileHelperRequest(cfg, newWriteFilePayload("notes/today.md", "hello"))
	if writeReq.WriteFile == nil {
		t.Fatal("expected WriteFile payload")
	}
	if got, want := writeReq.WriteFile.Path, "notes/today.md"; got != want {
		t.Fatalf("WriteFile.Path = %q, want %q", got, want)
	}
	if got, want := writeReq.WriteFile.Content, "hello"; got != want {
		t.Fatalf("WriteFile.Content = %q, want %q", got, want)
	}

	editReq := newEditFileHelperRequest(cfg, newEditFilePayload("main.go", "old", "new"))
	if editReq.EditFile == nil {
		t.Fatal("expected EditFile payload")
	}
	if got, want := editReq.EditFile.Path, "main.go"; got != want {
		t.Fatalf("EditFile.Path = %q, want %q", got, want)
	}
	if got, want := editReq.EditFile.OldText, "old"; got != want {
		t.Fatalf("EditFile.OldText = %q, want %q", got, want)
	}
	if got, want := editReq.EditFile.NewText, "new"; got != want {
		t.Fatalf("EditFile.NewText = %q, want %q", got, want)
	}

	patchReq := newApplyPatchHelperRequest(
		cfg,
		newApplyPatchPayload("*** Begin Patch\n*** End Patch"),
	)
	if patchReq.ApplyPatch == nil {
		t.Fatal("expected ApplyPatch payload")
	}
	if got, want := patchReq.ApplyPatch.Patch, "*** Begin Patch\n*** End Patch"; got != want {
		t.Fatalf("ApplyPatch.Patch = %q, want %q", got, want)
	}
}

func TestSandboxFileMethodsDecodeTypedResponses(t *testing.T) {
	t.Parallel()

	helperBin := writeFakeHelperBinary(t)
	sb := &Sandbox{
		cfg: Settings{
			ContainerName:    "q15-test",
			WorkspaceHostDir: "/tmp/q15-test",
			WorkspaceDir:     "/workspace",
			MemoryHostDir:    "/tmp/q15-test/.q15-memory",
			MemoryDir:        "/memory",
			SkillsHostDir:    "/tmp/q15-skills",
			SkillsDir:        "/skills",
		},
		helperBin: helperBin,
		prepared:  true,
	}

	ctx := context.Background()

	readResult, err := sb.ReadFile(ctx, "docs/readme.md", 2, 40)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := readResult.Content, "alpha\nbeta"; got != want {
		t.Fatalf("ReadFile().Content = %q, want %q", got, want)
	}
	if !readResult.Truncated || readResult.NextOffsetLines != 3 || readResult.TotalLines != 5 {
		t.Fatalf("ReadFile() result = %+v, want truncated next-offset metadata", readResult)
	}

	writeResult, err := sb.WriteFile(ctx, "notes/today.md", "hello")
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got, want := writeResult.Path, "/workspace/notes/today.md"; got != want {
		t.Fatalf("WriteFile().Path = %q, want %q", got, want)
	}
	if got, want := writeResult.BytesWritten, 5; got != want {
		t.Fatalf("WriteFile().BytesWritten = %d, want %d", got, want)
	}

	editResult, err := sb.EditFile(ctx, "main.go", "old", "new")
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if got, want := editResult.Path, "/workspace/main.go"; got != want {
		t.Fatalf("EditFile().Path = %q, want %q", got, want)
	}
	if got, want := editResult.FirstChangedLine, 12; got != want {
		t.Fatalf("EditFile().FirstChangedLine = %d, want %d", got, want)
	}
	if !strings.Contains(editResult.Diff, "-old") || !strings.Contains(editResult.Diff, "+new") {
		t.Fatalf("EditFile().Diff = %q, want compact diff", editResult.Diff)
	}

	patchResult, err := sb.ApplyPatch(ctx, "*** Begin Patch\n*** End Patch")
	if err != nil {
		t.Fatalf("ApplyPatch() error = %v", err)
	}
	if got, want := strings.Join(patchResult.ChangedFiles, ","), "/workspace/a.txt,/workspace/b.txt"; got != want {
		t.Fatalf("ApplyPatch().ChangedFiles = %q, want %q", got, want)
	}
	if got, want := patchResult.Summary, "applied patch to 2 file(s)"; got != want {
		t.Fatalf("ApplyPatch().Summary = %q, want %q", got, want)
	}
}

func newReadFilePayload(
	path string,
	offsetLines int,
	limitLines int,
) sandboxcontract.ReadFileRequest {
	return sandboxcontract.ReadFileRequest{
		Path:        path,
		OffsetLines: offsetLines,
		LimitLines:  limitLines,
	}
}

func newWriteFilePayload(path string, content string) sandboxcontract.WriteFileRequest {
	return sandboxcontract.WriteFileRequest{
		Path:    path,
		Content: content,
	}
}

func newEditFilePayload(
	path string,
	oldText string,
	newText string,
) sandboxcontract.EditFileRequest {
	return sandboxcontract.EditFileRequest{
		Path:    path,
		OldText: oldText,
		NewText: newText,
	}
}

func newApplyPatchPayload(patch string) sandboxcontract.ApplyPatchRequest {
	return sandboxcontract.ApplyPatchRequest{Patch: patch}
}

func writeFakeHelperBinary(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "fake-helper.sh")
	script := `#!/bin/sh
case "$1" in
  read-file)
    printf '%s\n' '{"read_file":{"content":"alpha\nbeta","truncated":true,"next_offset_lines":3,"total_lines":5}}'
    ;;
  write-file)
    printf '%s\n' '{"write_file":{"path":"/workspace/notes/today.md","bytes_written":5}}'
    ;;
  edit-file)
    printf '%s\n' '{"edit_file":{"path":"/workspace/main.go","diff":"-old\n+new","first_changed_line":12}}'
    ;;
  apply-patch)
    printf '%s\n' '{"apply_patch":{"changed_files":["/workspace/a.txt","/workspace/b.txt"],"diff":"=== /workspace/a.txt ===","summary":"applied patch to 2 file(s)"}}'
    ;;
  *)
    printf '%s\n' '{"error":"unexpected action"}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake helper) error = %v", err)
	}
	return path
}
