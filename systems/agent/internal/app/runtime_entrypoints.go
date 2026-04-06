package app

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type runtimeEntryPoints struct {
	modelClient          agent.ModelClient
	planner              modelselection.Planner
	tools                agent.ToolRegistry
	interactiveModelRefs []string
	cognitionModelRefs   []string
	interactivePrompt    string
	interactiveStore     agent.ConversationStore
	loader               cognition.ContextLoader
	recentTurns          int
}

func newRuntimeEntryPoints(
	modelClient agent.ModelClient,
	planner modelselection.Planner,
	tools agent.ToolRegistry,
	interactiveModelRefs []string,
	cognitionModelRefs []string,
	interactivePrompt string,
	interactiveStore agent.ConversationStore,
	loader cognition.ContextLoader,
	recentTurns int,
) *runtimeEntryPoints {
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	if len(cognitionModelRefs) == 0 {
		cognitionModelRefs = interactiveModelRefs
	}
	return &runtimeEntryPoints{
		modelClient:          modelClient,
		planner:              planner,
		tools:                tools,
		interactiveModelRefs: append([]string(nil), interactiveModelRefs...),
		cognitionModelRefs:   append([]string(nil), cognitionModelRefs...),
		interactivePrompt:    strings.TrimSpace(interactivePrompt),
		interactiveStore:     interactiveStore,
		loader:               loader,
		recentTurns:          recentTurns,
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
		r.interactiveModelRefs,
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
		r.cognitionModelRefs,
		r.loader,
	)
}
