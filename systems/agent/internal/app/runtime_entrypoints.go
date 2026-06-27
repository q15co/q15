package app

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type runtimeEntryPoints struct {
	modelClient                agent.ModelClient
	planner                    modelselection.Planner
	tools                      agent.ToolRegistry
	interactiveModelRefSource  agent.ModelRefSource
	cognitionRefResolver       cognition.ModelRefResolver
	interactivePrompt          string
	interactiveSystemTextHints []agent.SystemTextSource
	interactiveStore           agent.ConversationStore
	controllerStore            cognition.ControllerStore
	loader                     cognition.ContextLoader
	recentTurns                int
}

type runtimeEntryPointsConfig struct {
	modelClient                agent.ModelClient
	planner                    modelselection.Planner
	tools                      agent.ToolRegistry
	interactiveModelRefSource  agent.ModelRefSource
	cognitionRefResolver       cognition.ModelRefResolver
	interactivePrompt          string
	interactiveSystemTextHints []agent.SystemTextSource
	interactiveStore           agent.ConversationStore
	controllerStore            cognition.ControllerStore
	loader                     cognition.ContextLoader
	recentTurns                int
}

func newRuntimeEntryPoints(cfg runtimeEntryPointsConfig) *runtimeEntryPoints {
	if cfg.planner == nil {
		cfg.planner = modelselection.Passthrough{}
	}
	interactiveSystemTextHints := append(
		[]agent.SystemTextSource(nil),
		cfg.interactiveSystemTextHints...,
	)
	return &runtimeEntryPoints{
		modelClient:                cfg.modelClient,
		planner:                    cfg.planner,
		tools:                      cfg.tools,
		interactiveModelRefSource:  cfg.interactiveModelRefSource,
		cognitionRefResolver:       cfg.cognitionRefResolver,
		interactivePrompt:          strings.TrimSpace(cfg.interactivePrompt),
		interactiveSystemTextHints: interactiveSystemTextHints,
		interactiveStore:           cfg.interactiveStore,
		controllerStore:            cfg.controllerStore,
		loader:                     cfg.loader,
		recentTurns:                cfg.recentTurns,
	}
}

func (r *runtimeEntryPoints) NewInteractiveAgent() agent.Agent {
	if r == nil {
		return nil
	}
	return agent.NewLoopWithPlannerAndModelRefSource(
		r.modelClient,
		r.planner,
		r.tools,
		r.interactiveModelRefSource,
		r.interactivePrompt,
		r.interactiveStore,
		r.recentTurns,
		r.interactiveSystemTextHints...,
	)
}

func (r *runtimeEntryPoints) NewCognitionRunner() *cognition.Runner {
	if r == nil {
		return nil
	}
	return cognition.NewRunnerWithResolver(
		r.modelClient,
		r.planner,
		r.tools,
		r.cognitionRefResolver,
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
