package app

import (
	"context"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
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
