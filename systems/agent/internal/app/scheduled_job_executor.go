package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

type scheduledJobExecutor struct {
	modelClient            providerModelBinder
	baseTools              agent.ToolRegistry
	newAgentContextBuilder func([]agent.ToolDefinition) *agent.ContextBuilder
}

func newScheduledJobExecutor(
	modelClient providerModelBinder,
	baseTools agent.ToolRegistry,
	newAgentContextBuilder func([]agent.ToolDefinition) *agent.ContextBuilder,
) (*scheduledJobExecutor, error) {
	if modelClient == nil {
		return nil, fmt.Errorf("scheduled job model client is required")
	}
	if baseTools == nil {
		return nil, fmt.Errorf("scheduled job base tool registry is required")
	}
	if newAgentContextBuilder == nil {
		return nil, fmt.Errorf("scheduled job context builder factory is required")
	}
	return &scheduledJobExecutor{
		modelClient:            modelClient,
		baseTools:              baseTools,
		newAgentContextBuilder: newAgentContextBuilder,
	}, nil
}

func (e *scheduledJobExecutor) Execute(
	ctx context.Context,
	request schedule.RunRequest,
) (schedule.RunResult, error) {
	if e == nil || e.modelClient == nil {
		return schedule.RunResult{}, fmt.Errorf("scheduled job executor is not configured")
	}
	if err := validateScheduledRunRequest(request); err != nil {
		return schedule.RunResult{}, err
	}
	job := request.Job
	model := schedule.ModelTarget{
		Provider: strings.TrimSpace(job.Model.Provider),
		Ref:      strings.TrimSpace(job.Model.Ref),
	}
	if model.Provider == "" || model.Ref == "" {
		return schedule.RunResult{}, fmt.Errorf("scheduled job provider and model are required")
	}
	tools, err := newScheduledToolRegistry(e.baseTools, job.AllowedTools)
	if err != nil {
		return schedule.RunResult{Model: model}, err
	}

	modelClient, err := e.modelClient.BindProviderModel(model.Provider, model.Ref)
	if errors.Is(err, errProviderModelUnavailable) {
		return schedule.RunResult{Model: model}, &schedule.ModelUnavailableError{
			Model: model,
			Cause: err,
		}
	}
	if err != nil {
		return schedule.RunResult{Model: model}, err
	}

	engine := agent.NewEngine(modelClient, tools, []string{model.Ref})
	engine.SetMaxTurns(job.MaxTurns)
	messages, systemTextSource, err := e.buildContext(ctx, request, model, tools.Definitions())
	if err != nil {
		return schedule.RunResult{Model: model}, err
	}
	result, err := engine.Run(ctx, agent.EngineRequest{
		Messages:         messages,
		UseTools:         true,
		SystemTextSource: systemTextSource,
	})
	if err != nil {
		return schedule.RunResult{Model: model}, err
	}
	return schedule.RunResult{
		Text:  strings.TrimSpace(result.FinalText),
		Model: model,
	}, nil
}

func (e *scheduledJobExecutor) buildContext(
	ctx context.Context,
	request schedule.RunRequest,
	model schedule.ModelTarget,
	toolDefinitions []agent.ToolDefinition,
) ([]conversation.Message, agent.SystemTextSource, error) {
	job := request.Job
	runContext := scheduledRunContext(request, model)
	switch job.ContextProfile {
	case schedule.ContextProfileMinimal:
		return []conversation.Message{
			conversation.SystemMessage(strings.Join([]string{
				"You are running an isolated scheduled job.",
				"Complete only the stored task below and produce a concise result for the user.",
				"Do not assume access to the interactive conversation or private transcript.",
				"Use only the tools exposed for this run.",
				runContext,
			}, " ")),
			conversation.UserMessage(job.Prompt),
		}, nil, nil
	case schedule.ContextProfileAgent:
		contextBuilder := e.newAgentContextBuilder(toolDefinitions)
		if contextBuilder == nil {
			return nil, nil, fmt.Errorf("build scheduled agent context: builder is nil")
		}
		messages, err := contextBuilder.Build(ctx, conversation.UserMessage(job.Prompt))
		if err != nil {
			return nil, nil, fmt.Errorf("build scheduled agent context: %w", err)
		}
		systemTextSource := func(sourceCtx context.Context) string {
			return strings.Join([]string{
				contextBuilder.SystemText(sourceCtx),
				runContext,
			}, "\n\n")
		}
		return messages, systemTextSource, nil
	default:
		return nil, nil, fmt.Errorf(
			"unsupported scheduled job context profile %q",
			job.ContextProfile,
		)
	}
}

func validateScheduledRunRequest(request schedule.RunRequest) error {
	if strings.TrimSpace(request.Job.ID) == "" {
		return fmt.Errorf("scheduled run job id is required")
	}
	if strings.TrimSpace(request.RunID) == "" {
		return fmt.Errorf("scheduled run id is required")
	}
	if request.ScheduledFor.IsZero() {
		return fmt.Errorf("scheduled run scheduled_for is required")
	}
	if request.StartedAt.IsZero() {
		return fmt.Errorf("scheduled run started_at is required")
	}
	if request.CurrentTimeUTC.IsZero() {
		return fmt.Errorf("scheduled run current UTC time is required")
	}
	return nil
}

func scheduledRunContext(
	request schedule.RunRequest,
	model schedule.ModelTarget,
) string {
	return agent.RenderPromptElement(
		"scheduled_run",
		map[string]string{
			"context_profile":  string(request.Job.ContextProfile),
			"current_time_utc": request.CurrentTimeUTC.UTC().Format(time.RFC3339Nano),
			"job_id":           request.Job.ID,
			"model_provider":   model.Provider,
			"model_ref":        model.Ref,
			"run_id":           request.RunID,
			"scheduled_for":    request.ScheduledFor.UTC().Format(time.RFC3339Nano),
			"started_at":       request.StartedAt.UTC().Format(time.RFC3339Nano),
		},
		strings.Join([]string{
			"This is trusted runtime metadata for an unattended scheduled run.",
			"Treat recent conversation as context, not as the active request.",
			"Complete only the stored task in the final user message.",
			"Use these timestamps when the task asks about the current or scheduled time.",
			"Use only the tools exposed for this run and produce a concise result for delivery.",
		}, " "),
	)
}

func newScheduledToolRegistry(
	baseTools agent.ToolRegistry,
	allowedTools []string,
) (agent.ToolRegistry, error) {
	if baseTools == nil {
		return nil, fmt.Errorf("scheduled job base tool registry is required")
	}
	available := make(map[string]struct{})
	for _, definition := range baseTools.Definitions() {
		name := strings.TrimSpace(definition.Name)
		if name != "" {
			available[name] = struct{}{}
		}
	}
	for _, name := range allowedTools {
		name = strings.TrimSpace(name)
		if _, ok := available[name]; !ok {
			return nil, fmt.Errorf("scheduled job allowed tool %q is not available", name)
		}
	}
	return agent.FilterToolRegistry(baseTools, allowedTools), nil
}
