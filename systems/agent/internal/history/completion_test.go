package history

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func completionMessages(name string) []conversation.Message {
	return []conversation.Message{
		conversation.AssistantMessage(
			conversation.ToolCall("completion-child", name, `{"session_id":"child"}`),
		),
		conversation.ToolResultMessage(
			"completion-child",
			"Status: completed\nAnswer: Lantern",
			false,
		),
		conversation.AssistantMessage(
			conversation.ToolCall("verify", "subagent_read", `{"session_id":"child"}`),
		),
		conversation.ToolResultMessage("verify", "Verified child log", false),
		conversation.AssistantMessage(
			conversation.Text("The child returned Lantern.", conversation.TextDispositionFinal),
		),
	}
}

func assertCompletionSource(t *testing.T, source string) {
	t.Helper()
	for _, want := range []string{
		"runtime subagent_completion (automatically received, not assistant tool use or polling): Status: completed\nAnswer: Lantern",
		"assistant tool_call: subagent_read ",
		"tool tool_result: Verified child log",
		"The child returned Lantern.",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("source does not contain %q: %s", want, source)
		}
	}
	if strings.Count(source, "assistant tool_call:") != 1 {
		t.Fatalf("completion was counted as assistant tool use: %s", source)
	}
}

func TestRenderSourceDistinguishesCompletionFromActualRead(t *testing.T) {
	for _, name := range []string{conversation.SubagentCompletionName, "subagent_read"} {
		t.Run(name, func(t *testing.T) {
			assertCompletionSource(
				t,
				renderSource(conversation.Turn{Messages: completionMessages(name)}, 4096),
			)
		})
	}
}

func TestCompletionDigestPreservesRuntimeProvenanceAfterPersistence(t *testing.T) {
	for _, name := range []string{conversation.SubagentCompletionName, "subagent_read"} {
		t.Run(name, func(t *testing.T) {
			store := testStore(t)
			id, err := store.ReserveTurnSeq(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AppendTurnAtSeq(t.Context(), id, completionMessages(name)); err != nil {
				t.Fatal(err)
			}
			indexer, err := NewIndexer(
				t.Context(),
				store,
				EmbeddingSpace{
					Provider:   "fixture",
					Model:      "completion",
					Dimensions: 3,
				},
				&testEmbedder{},
				DigestFunc(func(_ context.Context, source string) (DigestResult, error) {
					assertCompletionSource(t, source)
					return DigestResult{
						Body:     "Received Lantern from the child and verified its log.",
						Provider: "fixture",
						Model:    "digest",
					}, nil
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := indexer.Digest(t.Context(), 1); err != nil || n != 1 {
				t.Fatalf("digest = %d, %v", n, err)
			}
		})
	}
}
