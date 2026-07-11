package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

func TestLoadImageRegistersImageAndReturnsMediaRef(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewLoadImage(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
		MemoryLocalDir:      t.TempDir(),
		MemoryRuntimeDir:    "/memory",
		SkillsLocalDir:      t.TempDir(),
		SkillsRuntimeDir:    "/skills",
	}, store)

	imagePath := filepath.Join(workspace, "artifact.png")
	if err := writeTestImage(imagePath); err != nil {
		t.Fatalf("writeTestImage() error = %v", err)
	}

	result, err := tool.RunResult(context.Background(), `{"path":"artifact.png"}`)
	if err != nil {
		t.Fatalf("RunResult() error = %v", err)
	}
	if !strings.Contains(result.Output, "Loaded image: /workspace/artifact.png") {
		t.Fatalf("Output = %q", result.Output)
	}
	if len(result.Attachments) != 1 ||
		!result.Attachments[0].IsMedia(conversation.MediaKindImage) ||
		!strings.HasPrefix(result.Attachments[0].MediaRef, "media://sha256/") {
		t.Fatalf("Attachments = %#v, want one image attachment", result.Attachments)
	}
	if len(result.MediaRefs) != 0 {
		t.Fatalf("MediaRefs = %#v, want empty (legacy field not used)", result.MediaRefs)
	}
}

func TestLoadImageRejectsNonImageFiles(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewLoadImage(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	textPath := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(textPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = tool.RunResult(context.Background(), `{"path":"note.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "does not appear to be an image") {
		t.Fatalf("RunResult() error = %v, want non-image failure", err)
	}
}

func writeTestImage(path string) error {
	return os.WriteFile(path, []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00,
		0x03, 0x01, 0x01, 0x00, 0xc9, 0xfe, 0x92, 0xef,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
		0xae, 0x42, 0x60, 0x82,
	}, 0o644)
}

func TestLoadImageMediaRefInjectsVisionAttachment(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Seed the store with an image via the store directly.
	sourcePath := filepath.Join(workspace, "inbound.png")
	if err := writeTestImage(sourcePath); err != nil {
		t.Fatalf("writeTestImage() error = %v", err)
	}
	seededRef, err := store.Store(sourcePath, q15media.Meta{
		Filename:    "inbound.png",
		ContentType: "image/png",
		Source:      "telegram",
	}, "telegram:1:1")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	tool := NewLoadImage(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	result, err := tool.RunResult(context.Background(), `{"media_ref":"`+seededRef+`"}`)
	if err != nil {
		t.Fatalf("RunResult() error = %v", err)
	}
	if !strings.Contains(result.Output, seededRef) {
		t.Fatalf("Output = %q, want seeded ref %q", result.Output, seededRef)
	}
	if len(result.Attachments) != 1 ||
		!result.Attachments[0].IsMedia(conversation.MediaKindImage) ||
		result.Attachments[0].MediaRef != seededRef {
		t.Fatalf("Attachments = %#v, want image attachment with seeded ref", result.Attachments)
	}
}

func TestLoadImageRejectsBothPathAndMediaRef(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewLoadImage(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	_, err = tool.RunResult(
		context.Background(),
		`{"path":"img.png","media_ref":"media://sha256/abc"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("RunResult() error = %v, want mutually exclusive failure", err)
	}
}

func TestLoadImageRejectsNeitherPathNorMediaRef(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewLoadImage(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	_, err = tool.RunResult(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("RunResult() error = %v, want exactly one failure", err)
	}
}
