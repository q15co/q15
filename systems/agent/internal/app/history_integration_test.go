package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/history"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

type historyFixtureEmbedder struct{}

func (historyFixtureEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func (historyFixtureEmbedder) EmbedDocuments(
	_ context.Context,
	input []embed.EmbeddingRequest,
) ([][]float32, error) {
	out := make([][]float32, len(input))
	for n := range out {
		out[n] = []float32{1, 0, 0}
	}
	return out, nil
}

func TestInteractiveAndScheduledContextUsePostgres(t *testing.T) {
	dsn := os.Getenv("Q15_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires the Postgres integration environment")
	}
	pg, err := history.Open(t.Context(), dsn, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	prior, err := pg.ReserveTurnSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.AppendTurnAtSeq(t.Context(), prior, []conversation.Message{conversation.UserMessage("What should store the conversation?"), conversation.AssistantMessage(conversation.Text("Would you like to use Postgres?", ""))}); err != nil {
		t.Fatal(err)
	}
	indexer, err := history.NewIndexer(
		t.Context(),
		pg,
		history.EmbeddingSpace{Provider: "fixture", Model: "runtime", Dimensions: 3},
		historyFixtureEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Chunk(t.Context(), 16); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Embed(t.Context(), 16); err != nil {
		t.Fatal(err)
	}
	service, err := history.NewContextService(indexer, history.ContextSettings{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "core/AGENT.md"), []byte("Authored identity."), 0600); err != nil {
		t.Fatal(err)
	}
	store := &runtimeStore{
		memory:         memory.NewStore(memoryrepo.New(root, nil), "fixture"),
		transcript:     pg,
		contextService: service,
	}
	model := &fakeModelClient{}
	tools, err := agent.NewToolRegistry()
	if err != nil {
		t.Fatal(err)
	}
	input := conversation.UserMessage("ok")
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	input.UserTemporal = &conversation.UserTemporalMetadata{TimeLocal: clock}
	loop := agent.NewLoop(model, tools, []string{"fixture"}, "Instructions.", store, 8)
	if _, err := loop.Reply(t.Context(), input, nil); err != nil {
		t.Fatal(err)
	}
	executor, err := newScheduledJobExecutor(
		&scheduledExecutorTestModel{client: model},
		tools,
		func(_ []agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder("Scheduled instructions.", store, 8)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	job := schedule.Job{
		ID:             "job-postgres",
		Prompt:         "Prepare a report about the choice.",
		Model:          schedule.ModelTarget{Provider: "fixture", Ref: "fixture"},
		ContextProfile: schedule.ContextProfileAgent,
		MaxTurns:       1,
	}
	if _, err := executor.Execute(t.Context(), scheduledExecutorRunRequest(job)); err != nil {
		t.Fatal(err)
	}

	if len(model.calls) != 2 {
		t.Fatalf("provider calls=%d", len(model.calls))
	}
	for _, call := range model.calls {
		var text strings.Builder
		for _, message := range call.messages {
			for _, part := range message.Parts {
				if part.Type == conversation.TextPartType {
					text.WriteString(part.Text)
				}
			}
		}
		if !strings.Contains(text.String(), "Would you like to use Postgres?") ||
			!strings.Contains(text.String(), "Authored identity.") {
			t.Fatal("provider request did not contain Postgres history and authored identity")
		}
	}
	page, err := pg.ListTurns(t.Context(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Turns) != 2 {
		t.Fatalf(
			"completed interactive turns=%d; scheduled execution should wait for delivery acknowledgement",
			len(page.Turns),
		)
	}
	saved := page.Turns[0].Messages[0]
	if conversation.TextValue(saved) != "ok" || saved.UserTemporal == nil ||
		!saved.UserTemporal.TimeLocal.Equal(clock) {
		t.Fatal("saved user input or clock changed")
	}
	if _, err := os.Stat(filepath.Join(root, "history")); !os.IsNotExist(err) {
		t.Fatal("runtime recreated file-backed history")
	}
}

type fittedHistoryStore struct {
	*history.Store
	service *history.ContextService
}

func (s fittedHistoryStore) PrepareContext(
	ctx context.Context,
	system, input []conversation.Message,
) (agent.PreparedContext, error) {
	return s.service.PrepareContext(ctx, system, input)
}

type largeHistoryTool struct{ output string }

func (largeHistoryTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "large_history_source",
		Description: "Read a large fixture source.",
		Parameters:  map[string]any{"type": "object"},
	}
}
func (t largeHistoryTool) Run(context.Context, string) (string, error) { return t.output, nil }

func TestPostgresKeepsLargeToolSourceWhileModelReplayFits(t *testing.T) {
	dsn := os.Getenv("Q15_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable Postgres")
	}
	store, err := history.Open(t.Context(), dsn, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	indexer, err := history.NewIndexer(
		t.Context(),
		store,
		history.EmbeddingSpace{Provider: "fixture", Model: "large-source", Dimensions: 3},
		historyFixtureEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := history.NewContextService(
		indexer,
		history.ContextSettings{InitialContextMaxTokens: 2048},
	)
	if err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("original large source output ", 40000)
	tools, err := agent.NewToolRegistry(largeHistoryTool{output: original})
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.ToolCall("large-call", "large_history_source", `{}`),
					),
				},
			},
		},
	}
	loop := agent.NewLoop(
		model,
		tools,
		[]string{"fixture"},
		"Instructions.",
		fittedHistoryStore{Store: store, service: service},
		4,
	)
	if _, err := loop.Reply(t.Context(), conversation.UserMessage("inspect the large source"), nil); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 2 {
		t.Fatalf("model calls=%d", len(model.calls))
	}
	tokens, err := agent.RequestEstimatedTokens(model.calls[1].messages, model.calls[1].tools)
	if err != nil || tokens > 2048 {
		t.Fatalf("provider replay unbounded: %d %v", tokens, err)
	}
	page, err := store.ListTurns(t.Context(), 0, 1)
	if err != nil || len(page.Turns) != 1 {
		t.Fatal("completed source missing", err)
	}
	found := false
	for _, m := range page.Turns[0].Messages {
		for _, p := range m.Parts {
			if p.Type == conversation.ToolResultPartType {
				found = p.Content == original
			}
		}
	}
	if !found {
		t.Fatal("bounded provider replay overwrote original stored tool output")
	}
}
