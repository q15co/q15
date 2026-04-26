package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type spyRuntimeStore struct {
	appendCalls int
}

func (s *spyRuntimeStore) LoadRecentMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return nil, nil
}

func (s *spyRuntimeStore) LoadLatestMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return nil, nil
}

func (s *spyRuntimeStore) LoadLastUserTimestamp(
	context.Context,
) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *spyRuntimeStore) AppendTurn(
	context.Context,
	[]conversation.Message,
) error {
	s.appendCalls++
	return nil
}

func (s *spyRuntimeStore) LoadCoreMemory(context.Context) (agent.CoreMemory, error) {
	return agent.CoreMemory{}, nil
}

func (s *spyRuntimeStore) LoadSemanticMemory(context.Context) (agent.SemanticMemory, error) {
	return agent.SemanticMemory{}, nil
}

func (s *spyRuntimeStore) LoadWorkingMemory(context.Context) (agent.WorkingMemory, error) {
	return agent.WorkingMemory{}, nil
}

func (s *spyRuntimeStore) LoadSkillCatalog(context.Context) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{}, nil
}

func (s *spyRuntimeStore) LoadCognitionArtifact(
	context.Context,
	string,
) (cognition.Artifact, error) {
	return cognition.Artifact{}, nil
}

func (s *spyRuntimeStore) StoreCognitionArtifact(
	context.Context,
	cognition.Artifact,
) error {
	return nil
}

func (s *spyRuntimeStore) LoadHead(context.Context) (int64, time.Time, error) {
	return int64(s.appendCalls), time.Now().UTC(), nil
}

func (s *spyRuntimeStore) LoadConsolidationCheckpoint(
	context.Context,
) (cognition.ConsolidationCheckpoint, error) {
	return cognition.ConsolidationCheckpoint{}, nil
}

func (s *spyRuntimeStore) LoadJobState(
	context.Context,
	string,
) (cognition.JobState, error) {
	return cognition.JobState{}, nil
}

func (s *spyRuntimeStore) StoreJobState(
	context.Context,
	string,
	cognition.JobState,
) error {
	return nil
}

func (s *spyRuntimeStore) StoreConsolidationCheckpoint(
	context.Context,
	cognition.ConsolidationCheckpoint,
) (cognition.ConsolidationCheckpoint, error) {
	return cognition.ConsolidationCheckpoint{}, nil
}

func (s *spyRuntimeStore) AppendRunRecord(
	context.Context,
	cognition.RunRecord,
) error {
	return nil
}

type appFakeCognitionJob struct{}

func (appFakeCognitionJob) Type() string {
	return "framework.probe"
}

func (appFakeCognitionJob) Build(context.Context, cognition.ContextLoader) (cognition.Spec, error) {
	return cognition.Spec{
		Objective:          "Exercise the cognition runner entrypoint.",
		CompletionContract: "Return `ok` when complete.",
	}, nil
}

func (appFakeCognitionJob) ApplyResult(
	context.Context,
	cognition.ContextLoader,
	cognition.JobOutput,
) (cognition.ParsedResult, error) {
	return cognition.ParsedResult{
		Summary: "ok",
	}, nil
}

func TestRuntimeEntryPointsBuildInteractiveAndCognitionPaths(t *testing.T) {
	store := &spyRuntimeStore{}
	model := &fakeModelClient{}
	entryPoints := newRuntimeEntryPoints(runtimeEntryPointsConfig{
		modelClient:          model,
		planner:              modelselection.Passthrough{},
		interactiveModelRefs: []string{"interactive"},
		cognitionModelRefs:   []string{"cognition"},
		interactivePrompt:    "Interactive prompt",
		interactiveStore:     store,
		controllerStore:      store,
		loader:               store,
		recentTurns:          3,
	})

	interactive := entryPoints.NewInteractiveAgent()
	if interactive == nil {
		t.Fatal("NewInteractiveAgent() = nil")
	}
	if _, err := interactive.Reply(
		context.Background(),
		conversation.UserMessage("hello"),
		nil,
	); err != nil {
		t.Fatalf("interactive Reply() error = %v", err)
	}
	if store.appendCalls != 1 {
		t.Fatalf("appendCalls after interactive reply = %d, want 1", store.appendCalls)
	}

	runner := entryPoints.NewCognitionRunner()
	if runner == nil {
		t.Fatal("NewCognitionRunner() = nil")
	}
	if _, err := runner.Run(context.Background(), appFakeCognitionJob{}, nil); err != nil {
		t.Fatalf("cognition Run() error = %v", err)
	}
	if store.appendCalls != 1 {
		t.Fatalf("appendCalls after cognition run = %d, want 1", store.appendCalls)
	}
	if len(model.calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.calls))
	}
	if model.calls[0].model != "interactive" {
		t.Fatalf("interactive model = %q, want %q", model.calls[0].model, "interactive")
	}
	if model.calls[1].model != "cognition" {
		t.Fatalf("cognition model = %q, want %q", model.calls[1].model, "cognition")
	}

	controller, err := entryPoints.NewCognitionController()
	if err != nil {
		t.Fatalf("NewCognitionController() error = %v", err)
	}
	if controller == nil {
		t.Fatal("NewCognitionController() = nil")
	}
}

func TestRuntimeEntryPointsInteractiveReplayUsesCheckpointAwareHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	mem := memory.NewStore(root, "Jared", &fakeMemoryCommitter{})
	if err := mem.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	appendTurn := func(userText, assistantText string) {
		t.Helper()
		if err := mem.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(userText),
			conversation.AssistantMessage(conversation.Text(assistantText, "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", userText, err)
		}
	}

	appendTurn("one", "first")
	appendTurn("two", "second")
	appendTurn("three", "third")

	if _, err := mem.StoreConsolidationCheckpoint(context.Background(), cognition.ConsolidationCheckpoint{
		LastConsolidatedSeq: 2,
	}); err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}

	store := &runtimeStore{memory: mem}
	model := &fakeModelClient{}
	entryPoints := newRuntimeEntryPoints(runtimeEntryPointsConfig{
		modelClient:          model,
		planner:              modelselection.Passthrough{},
		interactiveModelRefs: []string{"interactive"},
		cognitionModelRefs:   []string{"cognition"},
		interactivePrompt:    "Interactive prompt",
		interactiveStore:     store,
		controllerStore:      store,
		loader:               store,
		recentTurns:          6,
	})

	interactive := entryPoints.NewInteractiveAgent()
	if interactive == nil {
		t.Fatal("NewInteractiveAgent() = nil")
	}
	if _, err := interactive.Reply(
		context.Background(),
		conversation.UserMessage("new-question"),
		nil,
	); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}

	if len(model.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.calls))
	}
	gotInput := model.calls[0].messages
	if len(gotInput) < 4 {
		t.Fatalf("model input len = %d, want at least 4", len(gotInput))
	}
	if gotInput[0].Role != conversation.SystemRole {
		t.Fatalf("model input[0].Role = %q, want system", gotInput[0].Role)
	}
	if got := conversation.TextValue(gotInput[len(gotInput)-3]); got != "three" {
		t.Fatalf("model input[1] = %q, want checkpoint-relative replay", got)
	}
	if got := conversation.TextValue(gotInput[len(gotInput)-2]); got != "third" {
		t.Fatalf("model input[2] = %q, want checkpoint-relative replay", got)
	}
	if got := conversation.TextValue(gotInput[len(gotInput)-1]); got != "new-question" {
		t.Fatalf("model input[3] = %q, want current user input", got)
	}
}

type fakeMemoryCommitter struct{}

func (f *fakeMemoryCommitter) EnsureRepo(context.Context, string) error {
	return nil
}

func (f *fakeMemoryCommitter) CommitAll(context.Context, string, string) (string, error) {
	return "sha-test", nil
}
