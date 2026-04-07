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
	controllerStore      cognition.ControllerStore
	loader               cognition.ContextLoader
	recentTurns          int
}

type runtimeEntryPointsConfig struct {
	modelClient          agent.ModelClient
	planner              modelselection.Planner
	tools                agent.ToolRegistry
	interactiveModelRefs []string
	cognitionModelRefs   []string
	interactivePrompt    string
	interactiveStore     agent.ConversationStore
	controllerStore      cognition.ControllerStore
	loader               cognition.ContextLoader
	recentTurns          int
}

func newRuntimeEntryPoints(cfg runtimeEntryPointsConfig) *runtimeEntryPoints {
	if cfg.planner == nil {
		cfg.planner = modelselection.Passthrough{}
	}
	if len(cfg.cognitionModelRefs) == 0 {
		cfg.cognitionModelRefs = cfg.interactiveModelRefs
	}
	return &runtimeEntryPoints{
		modelClient:          cfg.modelClient,
		planner:              cfg.planner,
		tools:                cfg.tools,
		interactiveModelRefs: append([]string(nil), cfg.interactiveModelRefs...),
		cognitionModelRefs:   append([]string(nil), cfg.cognitionModelRefs...),
		interactivePrompt:    strings.TrimSpace(cfg.interactivePrompt),
		interactiveStore:     cfg.interactiveStore,
		controllerStore:      cfg.controllerStore,
		loader:               cfg.loader,
		recentTurns:          cfg.recentTurns,
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

func (r *runtimeEntryPoints) NewCognitionController(
	registrations ...cognition.JobRegistration,
) (*cognition.Controller, error) {
	if r == nil {
		return nil, nil
	}
	return cognition.NewController(
		r.NewCognitionRunner(),
		r.controllerStore,
		r.loader,
		registrations...,
	)
}
