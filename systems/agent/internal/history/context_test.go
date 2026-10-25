package history

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestContextFitsWholeTurnsAndKeepsCurrentClock(t *testing.T) {
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("", 7200))
	input := conversation.UserMessage("ok")
	input.UserTemporal = &conversation.UserTemporalMetadata{TimeLocal: clock}
	large := strings.Repeat("large tool output ", 10000)
	recent := []conversation.Message{
		conversation.UserMessage("inspect the file"),
		conversation.AssistantMessage(conversation.ToolCall("read-1", "read_file", `{"path":"a"}`)),
		{
			Role:  conversation.ToolRole,
			Parts: []conversation.Part{conversation.ToolResult("read-1", large, false)},
		},
		conversation.AssistantMessage(
			conversation.Text("The file is valid.", conversation.TextDispositionFinal),
		),
	}
	snapshot := contextSnapshot{
		systemCount: 1, input: []conversation.Message{input}, settings: ContextSettings{WorkingBudgetTokens: 5000, MaxRequestBytes: 5000},
		groups: []contextGroup{
			{
				id:        1,
				kind:      "recent",
				createdAt: clock.Add(-time.Hour),
				messages: []conversation.Message{
					conversation.UserMessage("earlier request"),
					conversation.AssistantMessage(conversation.Text("earlier result", "")),
				},
				digest: "An earlier request was completed.",
			},
			{id: 2, kind: "recent", createdAt: clock, messages: recent, protected: true},
		},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	model := agent.ModelContext{Ref: "ollama-cloud/test", MaxContextTokens: 40000}
	first, err := snapshot.fitRequest(t.Context(), model, base, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.fitRequest(t.Context(), model, base, nil, len(base))
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("identical snapshot did not produce identical requests", err)
	}
	size, err := agent.RequestEncodedBytes(first, nil)
	if err != nil || size > 5000 {
		t.Fatalf("request bytes = %d, %v", size, err)
	}
	last := first[len(first)-1]
	if conversation.TextValue(last) != "ok" || last.UserTemporal == nil ||
		!last.UserTemporal.TimeLocal.Equal(clock) {
		t.Fatalf("current acknowledgement or clock changed: %+v", last)
	}
	var calls, results int
	for _, message := range first {
		for _, part := range message.Parts {
			if part.Type == conversation.ToolCallPartType && part.ID == "read-1" {
				calls++
			}
			if part.Type == conversation.ToolResultPartType && part.ToolCallID == "read-1" {
				results++
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("tool pair split: calls=%d results=%d", calls, results)
	}
	if recent[2].Parts[0].Content != large {
		t.Fatal("provider fitting changed stored source evidence")
	}
}

func TestContextRebuildsForModelChangesAndToolAccumulation(t *testing.T) {
	input := conversation.UserMessage("continue")
	snapshot := contextSnapshot{
		systemCount: 1,
		input:       []conversation.Message{input},
		settings: ContextSettings{
			WorkingBudgetTokens: 16000, MaxRequestBytes: 16000,
			ModelBudgetTokens: map[string]int{"small": 500},
		},
		groups: []contextGroup{
			{id: 1, kind: "gap", digest: strings.Repeat("remember this earlier result ", 100)},
			{id: 2, kind: "retrieved_digest", excerpt: strings.Repeat("retrieved fact ", 300)},
		},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	generated := []conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall("call", "read", `{}`)),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call", strings.Repeat("result ", 10000), false),
			},
		},
	}
	request := append(conversation.CloneMessages(base), generated...)
	large, err := snapshot.fitRequest(
		t.Context(),
		agent.ModelContext{Ref: "large", MaxContextTokens: 400000},
		request,
		nil,
		len(base),
	)
	if err != nil {
		t.Fatal(err)
	}
	small, err := snapshot.fitRequest(
		t.Context(),
		agent.ModelContext{Ref: "small", MaxContextTokens: 8000},
		request,
		nil,
		len(base),
	)
	if err != nil {
		t.Fatal(err)
	}
	largeSize, _ := agent.RequestEncodedBytes(large, nil)
	smallSize, _ := agent.RequestEncodedBytes(small, nil)
	if smallSize >= largeSize {
		t.Fatalf("model change did not refit request: large=%d small=%d", largeSize, smallSize)
	}
	if generated[1].Parts[0].Content != strings.Repeat("result ", 10000) {
		t.Fatal("active transcript was modified")
	}
}

func TestContextRejectsIrreducibleInputWithoutDroppingIt(t *testing.T) {
	secret := strings.Repeat("private input ", 500)
	input := conversation.UserMessage(secret)
	snapshot := contextSnapshot{
		systemCount: 1,
		input:       []conversation.Message{input},
		settings:    ContextSettings{WorkingBudgetTokens: 1024},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	_, err := snapshot.fitRequest(context.Background(), agent.ModelContext{}, base, nil, len(base))
	var budget *agent.TokenBudgetError
	if !errors.As(err, &budget) || strings.Contains(err.Error(), "private input") {
		t.Fatalf("missing content-free budget error: %v", err)
	}
}

func TestRetrievalAnchorsAcknowledgementsWithoutDraggingNewQuestions(t *testing.T) {
	recent := []conversation.Turn{
		{
			Seq: 4,
			Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("Proposal to use Postgres", "")),
			},
		},
	}
	if query := retrievalQuery([]conversation.Message{conversation.UserMessage("ok")}, recent, map[int64]string{4: "Postgres proposal"}); !strings.Contains(
		query,
		"Postgres proposal",
	) {
		t.Fatalf("acknowledgement lost its context: %q", query)
	}
	if query := retrievalQuery([]conversation.Message{conversation.UserMessage("Where is Berlin?")}, recent, map[int64]string{4: "Postgres proposal"}); strings.Contains(
		query,
		"Postgres",
	) {
		t.Fatalf("new question inherited unrelated context: %q", query)
	}
}

func TestSmallContextWindowKeepsLastAssistant(t *testing.T) {
	store := testStore(t)
	assistant := appendTestTurn(t, store, "The last assistant reply is still needed.")
	for range 6 {
		id, err := store.ReserveTurnSeq(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AppendTurnAtSeq(t.Context(), id, []conversation.Message{conversation.UserMessage("An incomplete user-only attempt.")}); err != nil {
			t.Fatal(err)
		}
	}
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "window", Dimensions: 3},
		&testEmbedder{},
		DigestFunc(func(context.Context, string) (DigestResult, error) {
			return DigestResult{
				Body:     "Summarized earlier work.",
				Provider: "fixture",
				Model:    "summary",
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Digest(t.Context(), 32); err != nil {
		t.Fatal(err)
	}
	turns, digests, err := store.contextWindow(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || len(digests) != 2 {
		t.Fatalf("window headers=%d digests=%d", len(turns), len(digests))
	}
	for n, turn := range turns {
		needsSource := n == 0 || turn.Seq == assistant
		if needsSource != (len(turn.Messages) > 0) {
			t.Fatalf(
				"turn %d source loaded=%t, needed=%t",
				turn.Seq,
				len(turn.Messages) > 0,
				needsSource,
			)
		}
	}
	service, err := NewContextService(indexer, ContextSettings{RecentTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareContext(
		t.Context(),
		[]conversation.Message{conversation.SystemMessage("Instructions.")},
		[]conversation.Message{conversation.UserMessage("yes")},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range prepared.Messages {
		if conversation.TextValue(message) == "Recorded." {
			found = true
		}
	}
	if !found {
		t.Fatal("latest assistant context outside the recent window was discarded")
	}
}
