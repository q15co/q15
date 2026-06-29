package media

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestAdaptMediaToCapabilities_ImageKeptWhenSupported(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("describe this", ""),
			conversation.Image("media://sha256/abc", ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, Support{conversation.MediaKindImage: true}, nil)

	if len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("adapted parts = %#v, want 2 parts", got[0].Parts)
	}
	if !got[0].Parts[1].IsMedia(conversation.MediaKindImage) {
		t.Fatalf("part[1] is not image media: %#v", got[0].Parts[1])
	}
	if got[0].Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("part[1] MediaRef = %q", got[0].Parts[1].MediaRef)
	}
}

func TestAdaptMediaToCapabilities_ImageDowngradedWhenNotSupported(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("describe this", ""),
			conversation.Image("media://sha256/abc", ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, nil, nil)

	if len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("adapted parts = %#v, want 2 parts", got[0].Parts)
	}
	if got[0].Parts[1].Type != conversation.TextPartType {
		t.Fatalf("part[1] type = %q, want %q", got[0].Parts[1].Type, conversation.TextPartType)
	}
	text := got[0].Parts[1].Text
	if !contains(text, "[Media: image]") {
		t.Fatalf("downgraded hint missing kind: %q", text)
	}
	if !contains(text, "Media-Ref: media://sha256/abc") {
		t.Fatalf("downgraded hint missing ref: %q", text)
	}
}

func TestAdaptMediaToCapabilities_AudioDowngradedWhenNotSupported(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Audio("media://sha256/voice"),
		),
	}

	got := AdaptMediaToCapabilities(messages, Support{conversation.MediaKindImage: true}, nil)

	if len(got) != 1 || len(got[0].Parts) != 1 {
		t.Fatalf("adapted parts = %#v, want 1 part", got[0].Parts)
	}
	if got[0].Parts[0].Type != conversation.TextPartType {
		t.Fatalf("part[0] type = %q, want %q", got[0].Parts[0].Type, conversation.TextPartType)
	}
	if !contains(got[0].Parts[0].Text, "[Media: audio]") {
		t.Fatalf("downgraded hint missing kind: %q", got[0].Parts[0].Text)
	}
}

func TestAdaptMediaToCapabilities_GenericMediaAlwaysDowngraded(t *testing.T) {
	kinds := []conversation.MediaKind{
		conversation.MediaKindVideo,
		conversation.MediaKindDocument,
		conversation.MediaKindSticker,
		conversation.MediaKindAnimation,
		conversation.MediaKindVideoNote,
	}
	for _, kind := range kinds {
		messages := []conversation.Message{
			conversation.UserMessageParts(
				conversation.Media(kind, "media://sha256/"+string(kind)),
			),
		}

		got := AdaptMediaToCapabilities(messages, Support{
			conversation.MediaKindImage: true,
			conversation.MediaKindAudio: true,
		}, nil)

		if len(got) != 1 || len(got[0].Parts) != 1 {
			t.Fatalf("[%s] adapted parts = %#v, want 1 part", kind, got[0].Parts)
		}
		if got[0].Parts[0].Type != conversation.TextPartType {
			t.Fatalf("[%s] part type = %q, want text hint", kind, got[0].Parts[0].Type)
		}
		if !contains(got[0].Parts[0].Text, "[Media: "+string(kind)+"]") {
			t.Fatalf("[%s] hint missing kind: %q", kind, got[0].Parts[0].Text)
		}
	}
}

func TestAdaptMediaToCapabilities_TextPartsUntouched(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessage("hello"),
	}

	got := AdaptMediaToCapabilities(messages, Support{
		conversation.MediaKindImage: true,
		conversation.MediaKindAudio: true,
	}, nil)

	if got[0].Parts[0].Type != conversation.TextPartType ||
		got[0].Parts[0].Text != "hello" {
		t.Fatalf("text part changed: %#v", got[0].Parts[0])
	}
}

func TestAdaptMediaToCapabilities_ToolPartsUntouched(t *testing.T) {
	messages := []conversation.Message{
		{
			Role: conversation.AssistantRole,
			Parts: []conversation.Part{
				{
					Type:      conversation.ToolCallPartType,
					ID:        "call-1",
					Name:      "exec",
					Arguments: `{"command":"ls"}`,
				},
			},
		},
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				{
					Type:       conversation.ToolResultPartType,
					ToolCallID: "call-1",
					Content:    "output",
				},
			},
		},
	}

	got := AdaptMediaToCapabilities(messages, nil, nil)

	if got[0].Parts[0].Type != conversation.ToolCallPartType ||
		got[0].Parts[0].Name != "exec" {
		t.Fatalf("tool-call part changed: %#v", got[0].Parts[0])
	}
	if got[1].Parts[0].Type != conversation.ToolResultPartType ||
		got[1].Parts[0].Content != "output" {
		t.Fatalf("tool-result part changed: %#v", got[1].Parts[0])
	}
}

func TestAdaptMediaToCapabilities_CanonicalTranscriptUnmutated(t *testing.T) {
	original := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Image("media://sha256/abc", ""),
			conversation.Audio("media://sha256/voice"),
		),
	}

	_ = AdaptMediaToCapabilities(original, nil, nil)

	if !original[0].Parts[0].IsMedia(conversation.MediaKindImage) {
		t.Fatalf("canonical image part mutated: %#v", original[0].Parts[0])
	}
	if !original[0].Parts[1].IsMedia(conversation.MediaKindAudio) {
		t.Fatalf("canonical audio part mutated: %#v", original[0].Parts[1])
	}
}

func TestAdaptMediaToCapabilities_HintWithNilStore(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Image("media://sha256/abc", ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, nil, nil)

	hint := got[0].Parts[0].Text
	if !contains(hint, "[Media: image]") {
		t.Fatalf("hint missing kind: %q", hint)
	}
	if contains(hint, "File:") {
		t.Fatalf("nil store should omit File: line: %q", hint)
	}
}

func TestAdaptMediaToCapabilities_HintWithResolvingStore(t *testing.T) {
	store := newTestStore(t)

	src := filepath.Join(store.rootDir, "source")
	if err := os.WriteFile(src, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ref, err := store.Store(src, Meta{
		Filename:    "photo.jpg",
		ContentType: "image/jpeg",
		Source:      "test",
	}, "test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Image(ref, ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, nil, store)

	hint := got[0].Parts[0].Text
	if !contains(hint, "File:") {
		t.Fatalf("hint should include File: line when store resolves: %q", hint)
	}
	if !contains(hint, ref) {
		t.Fatalf("hint should include media ref: %q", hint)
	}
}

func TestSupportFromCapabilities(t *testing.T) {
	tests := []struct {
		name string
		caps modelcatalog.Capabilities
		want Support
	}{
		{
			name: "image declared and serialized",
			caps: modelcatalog.Capabilities{ImageInput: true},
			want: Support{conversation.MediaKindImage: true},
		},
		{
			name: "audio declared but no provider serializer yet",
			caps: modelcatalog.Capabilities{AudioInput: true},
			want: Support{},
		},
		{
			name: "image and audio declared; only image is inline-serializable",
			caps: modelcatalog.Capabilities{ImageInput: true, AudioInput: true},
			want: Support{conversation.MediaKindImage: true},
		},
		{
			name: "no media capabilities declared",
			caps: modelcatalog.Capabilities{},
			want: Support{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SupportFromCapabilities(tt.caps)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SupportFromCapabilities(%+v) = %#v, want %#v", tt.caps, got, tt.want)
			}
		})
	}
}

func TestAdaptMediaToCapabilities_EmptyMessages(t *testing.T) {
	got := AdaptMediaToCapabilities(nil, Support{conversation.MediaKindImage: true}, nil)
	if len(got) != 0 {
		t.Fatalf("adapted nil messages = %#v, want empty", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

type testStore struct {
	*FileStore
	rootDir string
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return &testStore{FileStore: store, rootDir: dir}
}
