package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestTokenEstimateCountsContentToolsAndMedia(t *testing.T) {
	plain := []conversation.Message{
		conversation.UserMessage(strings.Repeat("Readable English prose. ", 200)),
	}
	prose, err := RequestEstimatedTokens(plain, nil)
	if err != nil || prose <= 0 {
		t.Fatal(prose, err)
	}
	bytes, err := RequestEncodedBytes(plain, nil)
	if err != nil || prose >= bytes/2 {
		t.Fatalf("token estimate still behaves like bytes: tokens=%d bytes=%d", prose, bytes)
	}
	tools := []ToolDefinition{
		{
			Name:        "search",
			Description: strings.Repeat("Search history. ", 100),
			Parameters:  map[string]any{"type": "object"},
		},
	}
	withTools, err := RequestEstimatedTokens(plain, tools)
	if err != nil || withTools <= prose {
		t.Fatal("tool definitions not counted", err)
	}
	media := append(conversation.CloneMessages(plain), conversation.UserMessage("image"))
	media[1].Parts = append(media[1].Parts, conversation.Image("", "data:image/png;base64,AA=="))
	withMedia, err := RequestEstimatedTokens(media, nil)
	if err != nil || withMedia < prose+8192 {
		t.Fatal("media reserve not counted", err)
	}
	media[1].Parts[1].DataURL = "data:image/png;base64," + strings.Repeat("AAAA", 10000)
	larger, err := RequestEstimatedTokens(media, nil)
	if err != nil || larger != withMedia {
		t.Fatal("base64 transport bytes counted as text tokens", err)
	}
}

func TestTokenEstimateFallsBackWithoutDroppingTheBudget(t *testing.T) {
	messages := []conversation.Message{
		conversation.UserMessage(strings.Repeat("é日本語 ", 40)),
	}
	tools := []ToolDefinition{{Name: "read", Parameters: map[string]any{"type": "object"}}}
	fallback, err := requestEstimatedTokens(messages, tools, nil)
	if err != nil || fallback < len(conversation.TextValue(messages[0])) {
		t.Fatalf("missing tokenizer did not produce a conservative estimate: %d, %v", fallback, err)
	}
	failed, err := requestEstimatedTokens(messages, tools, func(string) (int, error) {
		return 0, errors.New("tokenizer unavailable")
	})
	if err != nil || failed != fallback {
		t.Fatalf(
			"tokenizer error did not use the byte fallback: got %d, want %d, err=%v",
			failed,
			fallback,
			err,
		)
	}
}
