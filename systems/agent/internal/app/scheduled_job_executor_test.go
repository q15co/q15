package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

type scheduledExecutorTestTool struct {
	name string
}

type scheduledExecutorTestModel struct {
	client   *fakeModelClient
	provider string
	ref      string
	err      error
}

type scheduledExecutorTestContextStore struct{}

var scheduledExecutorTestTime = time.Date(2026, time.July, 19, 8, 19, 0, 0, time.UTC)

func scheduledExecutorRunRequest(job schedule.Job) schedule.RunRequest {
	if job.ID == "" {
		job.ID = "job-test"
	}
	return schedule.RunRequest{
		Job:            job,
		RunID:          "run-test",
		ScheduledFor:   scheduledExecutorTestTime,
		StartedAt:      scheduledExecutorTestTime.Add(time.Second),
		CurrentTimeUTC: scheduledExecutorTestTime.Add(2 * time.Second),
	}
}

func (scheduledExecutorTestContextStore) LoadRecentMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return []conversation.Message{conversation.AssistantMessage(
		conversation.Text("recent assistant reply", conversation.TextDispositionFinal),
	)}, nil
}

func (scheduledExecutorTestContextStore) LoadCoreMemory(
	context.Context,
) (agent.CoreMemory, error) {
	return agent.CoreMemory{Files: []agent.CoreMemoryFile{
		{RelativePath: "core/AGENT.md", Content: "agent identity"},
	}}, nil
}

func (scheduledExecutorTestContextStore) LoadWorkingMemory(
	context.Context,
) (agent.WorkingMemory, error) {
	return agent.WorkingMemory{
		RelativePath: "working/WORKING_MEMORY.md",
		Content:      "active work",
	}, nil
}

func (scheduledExecutorTestContextStore) LoadSkillCatalog(
	context.Context,
) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{Entries: []agent.SkillCatalogEntry{
		{Name: "research", Description: "research skill"},
	}}, nil
}

func (m *scheduledExecutorTestModel) BindProviderModel(
	provider string,
	ref string,
) (agent.ModelClient, error) {
	m.provider = provider
	m.ref = ref
	if m.err != nil {
		return nil, m.err
	}
	return m.client, nil
}

func (t scheduledExecutorTestTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: t.name}
}

func (scheduledExecutorTestTool) Run(context.Context, string) (string, error) {
	return "ok", nil
}

func TestScheduledJobExecutorUsesPinnedModelAndRestrictedTools(t *testing.T) {
	model := &scheduledExecutorTestModel{client: &fakeModelClient{}}
	base, err := agent.NewToolRegistry(
		scheduledExecutorTestTool{name: "read_file"},
		scheduledExecutorTestTool{name: "exec"},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	executor, err := newScheduledJobExecutor(
		model,
		base,
		func([]agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder("agent prompt", nil, 1)
		},
	)
	if err != nil {
		t.Fatalf("newScheduledJobExecutor() error = %v", err)
	}

	result, err := executor.Execute(context.Background(), scheduledExecutorRunRequest(schedule.Job{
		ContextProfile: schedule.ContextProfileMinimal,
		Model:          schedule.ModelTarget{Provider: "provider", Ref: "model"},
		Prompt:         "Check the durable source.",
		MaxTurns:       4,
		AllowedTools:   []string{"read_file"},
	}))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Text != "ok" ||
		result.Model != (schedule.ModelTarget{Provider: "provider", Ref: "model"}) {
		t.Fatalf("Execute() result = %#v", result)
	}
	if model.provider != "provider" || model.ref != "model" {
		t.Fatalf("exact target = %q/%q, want provider/model", model.provider, model.ref)
	}
	if len(model.client.calls) != 1 {
		t.Fatalf("Complete calls = %d, want 1", len(model.client.calls))
	}
	call := model.client.calls[0]
	if call.model != "model" {
		t.Fatalf("model = %q, want model", call.model)
	}
	if len(call.tools) != 1 || call.tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v, want only read_file", call.tools)
	}
	if len(call.messages) != 2 ||
		call.messages[0].Role != conversation.SystemRole ||
		call.messages[1].Role != conversation.UserRole {
		t.Fatalf("messages = %#v, want isolated system/user pair", call.messages)
	}
	systemText := conversation.TextValue(call.messages[0])
	for _, want := range []string{
		`context_profile="minimal"`,
		`current_time_utc="2026-07-19T08:19:02Z"`,
		`job_id="job-test"`,
		`model_provider="provider"`,
		`model_ref="model"`,
		`run_id="run-test"`,
		`scheduled_for="2026-07-19T08:19:00Z"`,
		`started_at="2026-07-19T08:19:01Z"`,
	} {
		if !strings.Contains(systemText, want) {
			t.Fatalf("minimal system context = %q, want %q", systemText, want)
		}
	}
	if len(call.messages[1].Parts) != 1 ||
		!strings.Contains(call.messages[1].Parts[0].Text, "durable source") {
		t.Fatalf("user prompt = %#v", call.messages[1].Parts)
	}
}

func TestScheduledJobExecutorRejectsUnavailableConfiguredTool(t *testing.T) {
	base, err := agent.NewToolRegistry(scheduledExecutorTestTool{name: "read_file"})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	if _, err := newScheduledToolRegistry(
		base,
		[]string{"missing"},
	); err == nil || !strings.Contains(err.Error(), `"missing" is not available`) {
		t.Fatalf("newScheduledToolRegistry() error = %v", err)
	}
}

func TestScheduledJobExecutorAppliesEachJobsToolPolicy(t *testing.T) {
	model := &scheduledExecutorTestModel{client: &fakeModelClient{}}
	base, err := agent.NewToolRegistry(
		scheduledExecutorTestTool{name: "read_file"},
		scheduledExecutorTestTool{name: "exec"},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	executor, err := newScheduledJobExecutor(
		model,
		base,
		func([]agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder("agent prompt", nil, 1)
		},
	)
	if err != nil {
		t.Fatalf("newScheduledJobExecutor() error = %v", err)
	}

	requests := []schedule.RunRequest{
		scheduledExecutorRunRequest(schedule.Job{
			ID:             "job-no-tools",
			ContextProfile: schedule.ContextProfileMinimal,
			Model:          schedule.ModelTarget{Provider: "provider", Ref: "model"},
			Prompt:         "Respond without tools.",
			MaxTurns:       4,
			AllowedTools:   []string{},
		}),
		scheduledExecutorRunRequest(schedule.Job{
			ID:             "job-exec",
			ContextProfile: schedule.ContextProfileMinimal,
			Model:          schedule.ModelTarget{Provider: "provider", Ref: "model"},
			Prompt:         "Run the command.",
			MaxTurns:       4,
			AllowedTools:   []string{"exec"},
		}),
	}
	for index, request := range requests {
		request.RunID = fmt.Sprintf("run-%d", index)
		if _, err := executor.Execute(context.Background(), request); err != nil {
			t.Fatalf("Execute(job %d) error = %v", index, err)
		}
	}

	if got := model.client.calls[0].tools; len(got) != 0 {
		t.Fatalf("first job tools = %#v, want none", got)
	}
	if got := model.client.calls[1].tools; len(got) != 1 || got[0].Name != "exec" {
		t.Fatalf("second job tools = %#v, want only exec", got)
	}
}

func TestScheduledJobExecutorRequiresTrustedRunMetadata(t *testing.T) {
	base, err := agent.NewToolRegistry()
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	executor, err := newScheduledJobExecutor(
		&scheduledExecutorTestModel{client: &fakeModelClient{}},
		base,
		func([]agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder("agent prompt", nil, 1)
		},
	)
	if err != nil {
		t.Fatalf("newScheduledJobExecutor() error = %v", err)
	}

	_, err = executor.Execute(context.Background(), schedule.RunRequest{})
	if err == nil || !strings.Contains(err.Error(), "job id is required") {
		t.Fatalf("Execute() missing-metadata error = %v", err)
	}
}

func TestScheduledJobExecutorAgentProfileUsesFullContextWithRestrictedTools(t *testing.T) {
	model := &scheduledExecutorTestModel{client: &fakeModelClient{}}
	base, err := agent.NewToolRegistry(
		scheduledExecutorTestTool{name: "read_file"},
		scheduledExecutorTestTool{name: "exec"},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	executor, err := newScheduledJobExecutor(
		model,
		base,
		func(definitions []agent.ToolDefinition) *agent.ContextBuilder {
			names := make([]string, 0, len(definitions))
			for _, definition := range definitions {
				names = append(names, definition.Name)
			}
			return agent.NewContextBuilder(
				"full agent prompt with tools: "+strings.Join(names, ", "),
				scheduledExecutorTestContextStore{},
				3,
			)
		},
	)
	if err != nil {
		t.Fatalf("newScheduledJobExecutor() error = %v", err)
	}

	_, err = executor.Execute(context.Background(), scheduledExecutorRunRequest(schedule.Job{
		ID:             "job-context",
		ContextProfile: schedule.ContextProfileAgent,
		Model:          schedule.ModelTarget{Provider: "provider", Ref: "model"},
		Prompt:         "Use the context to prepare a report.",
		MaxTurns:       4,
		AllowedTools:   []string{"read_file"},
	}))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(model.client.calls) != 1 {
		t.Fatalf("Complete calls = %d, want 1", len(model.client.calls))
	}
	call := model.client.calls[0]
	if len(call.tools) != 1 || call.tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v, want only read_file", call.tools)
	}
	rendered := make([]string, 0, len(call.messages))
	for _, message := range call.messages {
		rendered = append(rendered, conversation.TextValue(message))
	}
	contextText := strings.Join(rendered, "\n")
	for _, want := range []string{
		"full agent prompt with tools: read_file",
		"agent identity",
		"research skill",
		"active work",
		"recent assistant reply",
		"Use the context to prepare a report.",
		`context_profile="agent"`,
		`current_time_utc="2026-07-19T08:19:02Z"`,
		`run_id="run-test"`,
		`scheduled_for="2026-07-19T08:19:00Z"`,
		`started_at="2026-07-19T08:19:01Z"`,
		`model_provider="provider"`,
	} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context = %q, want it to contain %q", contextText, want)
		}
	}
}

func TestScheduledJobExecutorClassifiesUnavailableExactTarget(t *testing.T) {
	base, err := agent.NewToolRegistry()
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	executor, err := newScheduledJobExecutor(
		&scheduledExecutorTestModel{
			client: &fakeModelClient{},
			err:    fmt.Errorf("%w: disappeared", errProviderModelUnavailable),
		},
		base,
		func([]agent.ToolDefinition) *agent.ContextBuilder {
			return agent.NewContextBuilder("agent prompt", nil, 1)
		},
	)
	if err != nil {
		t.Fatalf("newScheduledJobExecutor() error = %v", err)
	}

	target := schedule.ModelTarget{Provider: "provider", Ref: "model"}
	result, err := executor.Execute(context.Background(), scheduledExecutorRunRequest(schedule.Job{
		ContextProfile: schedule.ContextProfileMinimal,
		Model:          target,
		Prompt:         "Run later.",
		MaxTurns:       4,
	}))
	if !errors.Is(err, schedule.ErrModelUnavailable) {
		t.Fatalf("Execute() error = %v, want model unavailable", err)
	}
	if result.Model != target {
		t.Fatalf("Execute() model = %#v, want %#v", result.Model, target)
	}
}
