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

// testOGGOpusHeader is a minimal OGG header so http.DetectContentType returns
// application/ogg.
var testOGGOpusHeader = []byte{
	'O', 'g', 'g', 'S',
	0x00, 0x02,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00,
	0x01, 0x13,
	'O', 'p', 'u', 's', 'H', 'e', 'a', 'd',
}

func TestAttachAudioRegistersAudioAndReturnsAttachment(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewAttachAudio(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	audioPath := filepath.Join(workspace, "out.ogg")
	if err := os.WriteFile(audioPath, testOGGOpusHeader, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := tool.RunResult(context.Background(), `{"path":"out.ogg"}`)
	if err != nil {
		t.Fatalf("RunResult() error = %v", err)
	}
	if !strings.Contains(result.Output, "Attached audio: /workspace/out.ogg") {
		t.Fatalf("Output = %q", result.Output)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Type != conversation.AudioPartType ||
		!strings.HasPrefix(result.Attachments[0].MediaRef, "media://sha256/") {
		t.Fatalf("Attachments = %#v, want one audio attachment", result.Attachments)
	}
}

func TestAttachAudioRejectsNonAudioFiles(t *testing.T) {
	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	tool := NewAttachAudio(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	textPath := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(textPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = tool.RunResult(context.Background(), `{"path":"note.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "does not appear to be audio") {
		t.Fatalf("RunResult() error = %v, want non-audio failure", err)
	}
}
