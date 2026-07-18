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

func TestAttachMediaRegistersEveryOutboundKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		kind        conversation.MediaKind
		filename    string
		content     []byte
		contentType string
	}{
		{
			name:        "image",
			kind:        conversation.MediaKindImage,
			filename:    "chart.png",
			content:     testPNGHeader,
			contentType: "image/png",
		},
		{
			name:        "audio",
			kind:        conversation.MediaKindAudio,
			filename:    "voice.ogg",
			content:     testOGGOpusHeader,
			contentType: "audio/ogg",
		},
		{
			name:        "document",
			kind:        conversation.MediaKindDocument,
			filename:    "report.pdf",
			content:     []byte("%PDF-1.7\n"),
			contentType: "application/pdf",
		},
		{
			name:        "video",
			kind:        conversation.MediaKindVideo,
			filename:    "clip.mp4",
			content:     testMP4Header,
			contentType: "video/mp4",
		},
		{
			name:        "sticker",
			kind:        conversation.MediaKindSticker,
			filename:    "sticker.webp",
			content:     testWEBPHeader,
			contentType: "image/webp",
		},
		{
			name:        "animation",
			kind:        conversation.MediaKindAnimation,
			filename:    "loop.gif",
			content:     []byte("GIF89a\x01\x00\x01\x00"),
			contentType: "image/gif",
		},
		{
			name:        "video_note",
			kind:        conversation.MediaKindVideoNote,
			filename:    "note.mp4",
			content:     testMP4Header,
			contentType: "video/mp4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}
			tool := NewAttachMedia(fileops.Settings{
				WorkspaceLocalDir:   workspace,
				WorkspaceRuntimeDir: "/workspace",
			}, store)

			localPath := filepath.Join(workspace, tt.filename)
			if err := os.WriteFile(localPath, tt.content, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			result, err := tool.RunResult(
				context.Background(),
				`{"kind":"`+string(tt.kind)+`","path":"`+tt.filename+`"}`,
			)
			if err != nil {
				t.Fatalf("RunResult() error = %v", err)
			}
			if !strings.Contains(
				result.Output,
				"Attached "+attachMediaKinds[tt.kind].displayName+": /workspace/"+tt.filename,
			) {
				t.Fatalf("Output = %q", result.Output)
			}
			if len(result.Attachments) != 1 ||
				!result.Attachments[0].IsMedia(tt.kind) ||
				!strings.HasPrefix(result.Attachments[0].MediaRef, "media://sha256/") {
				t.Fatalf(
					"Attachments = %#v, want one %s attachment",
					result.Attachments,
					tt.kind,
				)
			}

			_, meta, err := store.Resolve(result.Attachments[0].MediaRef)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if meta.Filename != tt.filename ||
				meta.ContentType != tt.contentType ||
				meta.Source != attachMediaScope {
				t.Fatalf(
					"stored meta = %#v, want filename %q, content type %q, source %q",
					meta,
					tt.filename,
					tt.contentType,
					attachMediaScope,
				)
			}
		})
	}
}

func TestAttachMediaReusesStoredMediaRef(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	localPath := filepath.Join(workspace, "inbound.mp4")
	if err := os.WriteFile(localPath, testMP4Header, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	seededRef, err := store.Store(localPath, q15media.Meta{
		Filename:    "inbound.mp4",
		ContentType: "video/mp4",
		Source:      "telegram",
	}, "inbound")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	tool := NewAttachMedia(fileops.Settings{}, store)
	result, err := tool.RunResult(
		context.Background(),
		`{"kind":"video","media_ref":"`+seededRef+`"}`,
	)
	if err != nil {
		t.Fatalf("RunResult() error = %v", err)
	}
	if len(result.Attachments) != 1 ||
		!result.Attachments[0].IsMedia(conversation.MediaKindVideo) ||
		result.Attachments[0].MediaRef != seededRef {
		t.Fatalf("Attachments = %#v, want reused video ref %q", result.Attachments, seededRef)
	}
}

func TestAttachMediaRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tool := NewAttachMedia(fileops.Settings{
		WorkspaceLocalDir:   workspace,
		WorkspaceRuntimeDir: "/workspace",
	}, store)

	tests := []struct {
		name      string
		arguments string
		wantError string
	}{
		{
			name:      "unsupported kind",
			arguments: `{"kind":"unknown","path":"note.txt"}`,
			wantError: "unsupported media kind",
		},
		{
			name:      "neither source",
			arguments: `{"kind":"document"}`,
			wantError: "exactly one of path or media_ref is required",
		},
		{
			name:      "both sources",
			arguments: `{"kind":"document","path":"note.txt","media_ref":"media://sha256/nope"}`,
			wantError: "path and media_ref are mutually exclusive",
		},
		{
			name:      "non-video file",
			arguments: `{"kind":"video","path":"note.txt"}`,
			wantError: "incompatible content type",
		},
		{
			name:      "non-audio file",
			arguments: `{"kind":"audio","path":"note.txt"}`,
			wantError: "incompatible content type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tool.RunResult(context.Background(), tt.arguments)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("RunResult() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

var testPNGHeader = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
}

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

var testMP4Header = []byte{
	0x00, 0x00, 0x00, 0x18,
	'f', 't', 'y', 'p',
	'i', 's', 'o', 'm',
	0x00, 0x00, 0x02, 0x00,
	'i', 's', 'o', 'm',
	'i', 's', 'o', '2',
}

var testWEBPHeader = []byte{
	'R', 'I', 'F', 'F',
	0x0c, 0x00, 0x00, 0x00,
	'W', 'E', 'B', 'P',
	'V', 'P', '8', ' ',
}
