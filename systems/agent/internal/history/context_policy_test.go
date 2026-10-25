package history

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestWorkingBudgetUsesCatalogTokensAndReserves(t *testing.T) {
	snapshot := contextSnapshot{
		settings: ContextSettings{WorkingBudgetTokens: 64000, ContextWindowFraction: 0.25},
	}
	for _, tc := range []struct {
		name  string
		model agent.ModelContext
		want  int
	}{
		{"128k", agent.ModelContext{MaxContextTokens: 128000}, 32000},
		{"million", agent.ModelContext{MaxContextTokens: 1000000}, 64000},
		{"unknown", agent.ModelContext{}, 16000},
		{"small", agent.ModelContext{MaxContextTokens: 8192}, 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapshot.workingLimit(tc.model); got != tc.want {
				t.Fatalf("tokens=%d want=%d", got, tc.want)
			}
		})
	}
	snapshot.settings.ContextWindowFraction = 1
	if got := snapshot.workingLimit(agent.ModelContext{MaxContextTokens: 10000, MaxOutputTokens: 1000}); got != 8500 {
		t.Fatalf("reply and uncertainty reserves missing: %d", got)
	}
}

func TestAutomaticContextUsesRelevantSummariesWithoutGapWall(t *testing.T) {
	store := testStore(t)
	relevant := appendTestTurn(t, store, "The approved memory cutover uses PostgreSQL transcripts.")
	for range 12 {
		appendTestTurn(t, store, "Unrelated old weather conversation.")
	}
	latest := appendTestTurn(t, store, "We are ready for the next task.")
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "automatic", Dimensions: 3},
		&testEmbedder{},
		DigestFunc(func(_ context.Context, source string) (DigestResult, error) {
			target := source[strings.LastIndex(source, "TARGET turn "):]
			body := "Unrelated old weather summary."
			if strings.Contains(target, "PostgreSQL") {
				body = "The approved memory cutover uses PostgreSQL transcripts."
			}
			return DigestResult{Body: body, Provider: "fixture", Model: "summary"}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Chunk(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Digest(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Embed(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	service, err := NewContextService(indexer, ContextSettings{RecentTurns: 1, MinSimilarity: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareContext(
		t.Context(),
		[]conversation.Message{conversation.SystemMessage("Instructions.")},
		[]conversation.Message{conversation.UserMessage("What did we decide about PostgreSQL?")},
	)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, m := range prepared.Messages {
		text.WriteString(conversation.TextValue(m))
	}
	if strings.Contains(text.String(), "Unrelated old weather") {
		t.Fatal("unrelated gap summaries entered context")
	}
	if !strings.Contains(text.String(), "retrieved digest") ||
		!strings.Contains(text.String(), "PostgreSQL") {
		t.Fatal("relevant old summary missing")
	}
	result, err := indexer.Search(
		t.Context(),
		"PostgreSQL",
		SearchOptions{
			Mode:           "dense",
			SummariesOnly:  true,
			ExcludeTurnIDs: []int64{latest},
			MinSimilarity:  0.7,
		},
	)
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Kind != "digest" ||
		result.Hits[0].TurnID != relevant {
		t.Fatalf("automatic summary search: %+v %v", result, err)
	}
}

func TestTightContextDropsKeywordMatchesBeforeContinuity(t *testing.T) {
	input := conversation.UserMessage("continue")
	snapshot := contextSnapshot{
		systemCount: 1,
		input:       []conversation.Message{input},
		settings:    ContextSettings{WorkingBudgetTokens: 1024},
		groups: []contextGroup{
			{id: 1, kind: "gap", digest: "Keep this continuity summary."},
			{
				id:      2,
				kind:    "retrieved_digest",
				excerpt: strings.Repeat("weaker keyword match ", 700),
			},
		},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions"), input}
	out, err := snapshot.fitRequest(t.Context(), agent.ModelContext{}, base, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, m := range out {
		text.WriteString(conversation.TextValue(m))
	}
	if !strings.Contains(text.String(), "Keep this continuity") ||
		strings.Contains(text.String(), "weaker keyword") {
		t.Fatal("eviction lost continuity before keyword retrieval")
	}
}

func TestManyActiveToolResultsDegradeWithoutChangingSource(t *testing.T) {
	input := conversation.UserMessage("inspect these results")
	snapshot := contextSnapshot{
		systemCount: 1,
		input:       []conversation.Message{input},
		settings:    ContextSettings{WorkingBudgetTokens: 1500},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions"), input}
	active := []conversation.Message{}
	for range 8 {
		active = append(
			active,
			conversation.AssistantMessage(conversation.ToolCall("call", "read", `{}`)),
			conversation.Message{
				Role: conversation.ToolRole,
				Parts: []conversation.Part{
					conversation.ToolResult("call", strings.Repeat("large result ", 1000), false),
				},
			},
		)
	}
	out, err := snapshot.fitRequest(
		t.Context(),
		agent.ModelContext{},
		append(conversation.CloneMessages(base), active...),
		nil,
		len(base),
	)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := agent.RequestEstimatedTokens(out, nil)
	if err != nil || tokens > 1500 {
		t.Fatalf("unbounded active request: %d %v", tokens, err)
	}
	if !strings.Contains(active[1].Parts[0].Content, "large result") ||
		len(active[1].Parts[0].Content) < 10000 {
		t.Fatal("stored source was changed")
	}
}

func TestHistoryShareChangesRecentVersusTopicSelection(t *testing.T) {
	input := conversation.UserMessage("continue this task")
	groups := []contextGroup{
		{
			id:   1,
			kind: "recent",
			messages: []conversation.Message{
				conversation.UserMessage(
					"OLD_ORIGINAL " + strings.Repeat("older original evidence ", 400),
				),
			},
			digest: "OLD_SUMMARY: earlier work was recorded.",
		},
		{
			id:   2,
			kind: "recent",
			messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("Latest task context.", "")),
			},
			protected: true,
		},
		{
			id:      3,
			kind:    "retrieved_digest",
			excerpt: "RETRIEVED_TOPIC " + strings.Repeat("topic related fact ", 500),
		},
	}
	base := []conversation.Message{conversation.SystemMessage("Instructions"), input}
	for _, tc := range []struct {
		name         string
		share        float64
		wantOriginal bool
		wantTopic    bool
	}{
		{"prefer topic", 0.1, false, true}, {"prefer recency", 0.9, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := contextSnapshot{
				systemCount: 1,
				input:       []conversation.Message{input},
				settings: ContextSettings{
					WorkingBudgetTokens:   2600,
					RecentHistoryFraction: tc.share,
				},
				groups: groups,
			}
			out, err := snapshot.fitRequest(t.Context(), agent.ModelContext{}, base, nil, len(base))
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, m := range out {
				text.WriteString(conversation.TextValue(m))
			}
			if strings.Contains(text.String(), "OLD_ORIGINAL") != tc.wantOriginal ||
				strings.Contains(text.String(), "RETRIEVED_TOPIC") != tc.wantTopic {
				t.Fatalf("history share did not change selection: %s", text.String())
			}
			if !strings.Contains(text.String(), "Latest task context") {
				t.Fatal("current work lost")
			}
		})
	}
}
