package history

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestHistoryReadPagesPayloadsAndReplayWithoutLoadingWholeTurn(t *testing.T) {
	store := testStore(t)
	id, err := store.ReserveTurnSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("λ original output ", 10000) + "\x00end"
	messages := []conversation.Message{
		conversation.UserMessage("Read source"),
		conversation.AssistantMessage(conversation.ToolCall("call", "inspect", `{"path":"x"}`)),
		{
			Role:  conversation.ToolRole,
			Parts: []conversation.Part{conversation.ToolResult("call", original, false)},
		},
		conversation.AssistantMessage(
			conversation.Reasoning(
				"portable",
				map[string]json.RawMessage{
					"provider": json.RawMessage(`{"signature":"opaque+signed/value=="}`),
				},
			),
		),
	}
	if err := store.AppendTurnAtSeq(t.Context(), id, messages); err != nil {
		t.Fatal(err)
	}
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "pages", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := recallTool{indexer: indexer, read: true}
	cursor := sourceCursor{}
	var resultBody strings.Builder
	seenReplay := false
	for page := 0; page < 200; page++ {
		args, _ := json.Marshal(struct {
			TurnID int64 `json:"turn_id"`
			sourceCursor
			Limit int `json:"limit"`
		}{id, cursor, 4096})
		raw, err := tool.Run(t.Context(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 32768 {
			t.Fatalf("page exceeds result bound: %d", len(raw))
		}
		var out struct {
			Kind     string        `json:"kind"`
			Field    string        `json:"field"`
			Encoding string        `json:"encoding"`
			Source   string        `json:"source"`
			Next     *sourceCursor `json:"next"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		if out.Kind == "tool_result" {
			value := out.Source
			if out.Encoding == "base64" {
				body, err := base64.StdEncoding.DecodeString(value)
				if err != nil {
					t.Fatal(err)
				}
				value = string(body)
			}
			resultBody.WriteString(value)
		}
		if out.Field == "replay" {
			seenReplay = strings.Contains(out.Source, "opaque+signed/value==")
		}
		if out.Next == nil {
			break
		}
		cursor = *out.Next
	}
	if resultBody.String() != original || !seenReplay {
		t.Fatal("source pages lost original output or opaque replay")
	}
	for _, arg := range []string{fmt.Sprintf(`{"turn_id":%d,"limit":4097}`, id), fmt.Sprintf(`{"turn_id":%d,"offset":2147483647}`, id)} {
		if _, err := tool.Run(t.Context(), arg); err == nil {
			t.Fatal("unbounded cursor accepted")
		}
	}
}
