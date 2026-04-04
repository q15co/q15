package app

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type runtimeEntryPoints struct {
	modelClient       agent.ModelClient
	planner           modelselection.Planner
	tools             agent.ToolRegistry
	modelRefs         []string
	interactivePrompt string
	interactiveStore  agent.ConversationStore
	loader            cognition.ContextLoader
	recentTurns       int
}

func newRuntimeEntryPoints(
	modelClient agent.ModelClient,
	planner modelselection.Planner,
	tools agent.ToolRegistry,
	modelRefs []string,
	interactivePrompt string,
	interactiveStore agent.ConversationStore,
	loader cognition.ContextLoader,
	recentTurns int,
) *runtimeEntryPoints {
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	return &runtimeEntryPoints{
		modelClient:       modelClient,
		planner:           planner,
		tools:             tools,
		modelRefs:         append([]string(nil), modelRefs...),
		interactivePrompt: strings.TrimSpace(interactivePrompt),
		interactiveStore:  interactiveStore,
		loader:            loader,
		recentTurns:       recentTurns,
	}
}

func (r *runtimeEntryPoints) NewInteractiveAgent() agent.Agent {
	if r == nil {
		return nil
	}
	return agent.NewLoopWithPlanner(
		r.modelClient,
		r.planner,
		r.tools,
		r.modelRefs,
		r.interactivePrompt,
		r.interactiveStore,
		r.recentTurns,
	)
}

func (r *runtimeEntryPoints) NewCognitionRunner() *cognition.Runner {
	if r == nil {
		return nil
	}
	return cognition.NewRunnerWithPlanner(
		r.modelClient,
		r.planner,
		r.tools,
		r.modelRefs,
		r.loader,
	)
}
