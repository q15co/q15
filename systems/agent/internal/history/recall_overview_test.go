package history

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestHistoryReadStartsWithConversationAndPagesToolEvidence(t *testing.T) {
	store := testStore(t)
	id, err := store.ReserveTurnSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	question := "What did the benchmark establish?"
	answer := "The benchmark report: " + strings.Repeat("verified result λ ", 300)
	calls := []conversation.Part{
		conversation.Reasoning("private intermediate reasoning", nil),
		conversation.Text("Starting the test.", conversation.TextDispositionCommentary),
	}
	for n := range 20 {
		calls = append(
			calls,
			conversation.ToolCall(
				fmt.Sprintf("call-%d", n),
				"decision_check",
				fmt.Sprintf(`{"question":"item %d"}`, n),
			),
		)
	}
	messages := []conversation.Message{
		conversation.UserMessage(question),
		conversation.AssistantMessage(calls...),
	}
	for n := range 20 {
		messages = append(
			messages,
			conversation.Message{
				Role: conversation.ToolRole,
				Parts: []conversation.Part{
					conversation.ToolResult(
						fmt.Sprintf("call-%d", n),
						fmt.Sprintf(`{"choice":"result %d"}`, n),
						false,
					),
				},
			},
		)
	}
	messages = append(
		messages,
		conversation.AssistantMessage(
			conversation.Reasoning("write the final report", nil),
			conversation.Text(answer, conversation.TextDispositionFinal),
		),
	)
	if err := store.AppendTurnAtSeq(t.Context(), id, messages); err != nil {
		t.Fatal(err)
	}
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "overview", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	tool := recallTool{indexer: indexer, read: true}
	raw, err := tool.Run(t.Context(), fmt.Sprintf(`{"turn_id":%d}`, id))
	if err != nil {
		t.Fatal(err)
	}
	var overview recallOverview
	if err := json.Unmarshal([]byte(raw), &overview); err != nil {
		t.Fatal(err)
	}
	if len(raw) > recallResultBytes || len(overview.Conversation) != 2 ||
		overview.Conversation[0].Source != question ||
		overview.Conversation[1].Role != "assistant" ||
		!strings.HasPrefix(answer, overview.Conversation[1].Source) ||
		overview.Conversation[1].Source == "" {
		t.Fatalf("unexpected overview: %s", raw)
	}
	if strings.Contains(raw, "private intermediate reasoning") ||
		strings.Contains(raw, "Starting the test.") {
		t.Fatal("intermediate reasoning or commentary entered conversation overview")
	}
	report := overview.Conversation[1].Source
	next := overview.Conversation[1].Next
	for next != nil {
		args, _ := json.Marshal(next)
		raw, err := tool.Run(t.Context(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		var page sourcePage
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatal(err)
		}
		report += page.Source
		next = page.Next
	}
	if report != answer {
		t.Fatal("answer pagination lost source text")
	}
	var evidence []toolEvidence
	for page := 0; page < 25; page++ {
		evidence = append(evidence, overview.Evidence...)
		if overview.NextEvidence == nil {
			break
		}
		args, _ := json.Marshal(overview.NextEvidence)
		raw, err := tool.Run(t.Context(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		overview = recallOverview{}
		if err := json.Unmarshal([]byte(raw), &overview); err != nil {
			t.Fatal(err)
		}
		if len(overview.Conversation) != 0 {
			t.Fatal("evidence pagination repeated the conversation")
		}
	}
	if len(evidence) != 20 {
		t.Fatalf("got %d evidence entries, want 20", len(evidence))
	}
	for n, item := range evidence {
		args, _ := json.Marshal(item.Read)
		raw, err := tool.Run(t.Context(), string(args))
		if err != nil {
			t.Fatal(err)
		}
		var page sourcePage
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatal(err)
		}
		if item.Tool != "decision_check" ||
			page.Source != fmt.Sprintf(`{"choice":"result %d"}`, n) {
			t.Fatalf("evidence reference did not locate the original: %+v %+v", item, page)
		}
	}
	for _, tc := range []struct{ arguments, want string }{
		{fmt.Sprintf(`{"turn_id":%d,"limit":24000}`, id), "between 1 and 4096"},
		{fmt.Sprintf(`{"turn_id":%d,"part_id":9223372036854775807}`, id), "copy the returned read arguments"},
	} {
		if _, err := tool.Run(t.Context(), tc.arguments); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Fatalf("unhelpful read error: %v", err)
		}
	}
}

func TestRecallContextCapsPreserveJSONAndExactContinuation(t *testing.T) {
	for _, body := range []string{strings.Repeat("λ\"\\\n", 1200), strings.Repeat("plain text ", 1000)} {
		page := sourcePage{
			TurnID:   7,
			PartID:   9,
			Field:    "body",
			Encoding: "utf8",
			Offset:   42,
			Source:   body,
		}
		raw, err := encodeSourcePage(page, recallResultBytes)
		if err != nil {
			t.Fatal(err)
		}
		messages := []conversation.Message{
			conversation.AssistantMessage(
				conversation.ToolCall(
					"read",
					"history_read",
					`{"turn_id":7,"part_id":9,"offset":42}`,
				),
			),
			{
				Role:  conversation.ToolRole,
				Parts: []conversation.Part{conversation.ToolResult("read", raw, false)},
			},
		}
		bounded := capParts(messages, 1024, 512)
		content := bounded[1].Parts[0].Content
		var result sourcePage
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatalf("context cap broke history JSON: %s", content)
		}
		if len(content) > 1024 || result.Source == "" || !strings.HasPrefix(body, result.Source) ||
			result.Next == nil ||
			result.Next.Offset != 42+len([]rune(result.Source)) ||
			result.Next.TurnID != 7 ||
			result.Next.PartID != 9 {
			t.Fatalf("context cap lost source or continuation: %s", content)
		}
		if messages[1].Parts[0].Content != raw {
			t.Fatal("context cap changed recorded tool output")
		}
	}
}

func TestSearchResultCapsKeepReadArguments(t *testing.T) {
	result := SearchResult{
		Hits: []SearchHit{
			{
				TurnID: 7,
				Kind:   "chunk",
				Text:   strings.Repeat("quoted \"evidence\" ", 1000),
				Read:   &sourceCursor{TurnID: 7, PartID: 9, Field: "body"},
			},
		},
	}
	raw, err := encodeSearchResult(result, recallResultBytes)
	if err != nil {
		t.Fatal(err)
	}
	content := boundedRecallResult(raw, "history_search", 1024)
	var bounded SearchResult
	if err := json.Unmarshal([]byte(content), &bounded); err != nil {
		t.Fatal(err)
	}
	if len(content) > 1024 || !bounded.Truncated || len(bounded.Hits) != 1 ||
		bounded.Hits[0].Read.PartID != 9 {
		t.Fatalf("search cap lost source reference: %s", content)
	}
}

func TestHistorySearchProvidesDirectSourceReadArguments(t *testing.T) {
	store := testStore(t)
	id := appendTestTurn(t, store, "Original PostgreSQL source_identifier evidence.")
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "source-refs", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Chunk(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	result, err := indexer.Search(t.Context(), "source_identifier", SearchOptions{Mode: "lexical"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Read == nil ||
		result.Hits[0].Read.PartID <= 0 ||
		result.Hits[0].Read.TurnID != id {
		t.Fatalf("source search reference: %+v %v", result, err)
	}
	args, _ := json.Marshal(result.Hits[0].Read)
	raw, err := (recallTool{indexer: indexer, read: true}).Run(t.Context(), string(args))
	if err != nil {
		t.Fatal(err)
	}
	var page sourcePage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	if page.Source != "Original PostgreSQL source_identifier evidence." {
		t.Fatalf("source reference did not open original: %s", raw)
	}
}
