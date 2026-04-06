package app

import (
	"context"
	"testing"

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

func (appFakeCognitionJob) ParseResult(
	context.Context,
	cognition.JobOutput,
) (cognition.ParsedResult, error) {
	return cognition.ParsedResult{
		Summary: "ok",
	}, nil
}

func TestRuntimeEntryPointsBuildInteractiveAndCognitionPaths(t *testing.T) {
	store := &spyRuntimeStore{}
	model := &fakeModelClient{}
	entryPoints := newRuntimeEntryPoints(
		model,
		modelselection.Passthrough{},
		nil,
		[]string{"primary"},
		"Interactive prompt",
		store,
		store,
		3,
	)

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
}
