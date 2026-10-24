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
