package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestAdaptMediaToCapabilities_ImageKeptWhenSupported(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("describe this", ""),
			conversation.Image("media://sha256/abc", ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, Support{Image: true}, nil)

	if len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("adapted parts = %#v, want 2 parts", got[0].Parts)
	}
	if got[0].Parts[1].Type != conversation.ImagePartType {
		t.Fatalf("part[1] type = %q, want %q", got[0].Parts[1].Type, conversation.ImagePartType)
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

	got := AdaptMediaToCapabilities(messages, Support{Image: false}, nil)

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

func TestAdaptMediaToCapabilities_AudioAlwaysDowngradedInPhase1(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Audio("media://sha256/voice"),
		),
	}

	// Phase 1: AudioInput not declared on any model, so support.Audio is false.
	got := AdaptMediaToCapabilities(messages, Support{Image: true, Audio: false}, nil)

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

func TestAdaptMediaToCapabilities_AudioKeptWhenSupported(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Audio("media://sha256/voice"),
		),
	}

	got := AdaptMediaToCapabilities(messages, Support{Audio: true}, nil)

	if len(got) != 1 || len(got[0].Parts) != 1 {
		t.Fatalf("adapted parts = %#v, want 1 part", got[0].Parts)
	}
	if got[0].Parts[0].Type != conversation.AudioPartType {
		t.Fatalf("part[0] type = %q, want %q", got[0].Parts[0].Type, conversation.AudioPartType)
	}
}

func TestAdaptMediaToCapabilities_TextPartsUntouched(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessage("hello"),
	}

	got := AdaptMediaToCapabilities(messages, Support{Image: true, Audio: true}, nil)

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

	got := AdaptMediaToCapabilities(messages, Support{Image: false, Audio: false}, nil)

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

	_ = AdaptMediaToCapabilities(original, Support{Image: false, Audio: false}, nil)

	// The original slice must still contain typed parts — not text hints.
	if original[0].Parts[0].Type != conversation.ImagePartType {
		t.Fatalf("canonical image part mutated: %q", original[0].Parts[0].Type)
	}
	if original[0].Parts[1].Type != conversation.AudioPartType {
		t.Fatalf("canonical audio part mutated: %q", original[0].Parts[1].Type)
	}
}

func TestAdaptMediaToCapabilities_HintWithNilStore(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Image("media://sha256/abc", ""),
		),
	}

	got := AdaptMediaToCapabilities(messages, Support{Image: false}, nil)

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

	// Store a dummy file so Resolve succeeds.
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

	got := AdaptMediaToCapabilities(messages, Support{Image: false}, store)

	hint := got[0].Parts[0].Text
	if !contains(hint, "File:") {
		t.Fatalf("hint should include File: line when store resolves: %q", hint)
	}
	if !contains(hint, ref) {
		t.Fatalf("hint should include media ref: %q", hint)
	}
}

func TestAdaptMediaToCapabilities_EmptyMessages(t *testing.T) {
	got := AdaptMediaToCapabilities(nil, Support{Image: true}, nil)
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
