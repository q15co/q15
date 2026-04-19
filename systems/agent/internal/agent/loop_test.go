package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type fakeModelClient struct {
	results    []ModelClientResult
	callMsgs   [][]conversation.Message
	callModels []string
	complete   func(context.Context, string, []conversation.Message, []ToolDefinition) (ModelClientResult, error)
}

type failingModelClient struct {
	err error
}

type fakePlanner struct {
	plan                func([]string, modelselection.Requirements) (modelselection.Plan, error)
	plannedModelRefs    [][]string
	plannedRequirements []modelselection.Requirements
}

func (f *fakeModelClient) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []ToolDefinition,
) (ModelClientResult, error) {
	f.callMsgs = append(f.callMsgs, copyMessages(messages))
	f.callModels = append(f.callModels, model)
	if f.complete != nil {
		return f.complete(ctx, model, messages, tools)
	}
	_ = ctx
	_ = tools
	if len(f.results) == 0 {
		return assistantResult("ok"), nil
	}
	out := f.results[0]
	f.results = f.results[1:]
	return out, nil
}

func (f *fakePlanner) Plan(
	modelRefs []string,
	requirements modelselection.Requirements,
) (modelselection.Plan, error) {
	f.plannedModelRefs = append(f.plannedModelRefs, append([]string(nil), modelRefs...))
	f.plannedRequirements = append(f.plannedRequirements, requirements)
	if f.plan != nil {
		return f.plan(modelRefs, requirements)
	}
	return modelselection.Plan{
		EligibleRefs: append([]string(nil), modelRefs...),
	}, nil
}

func (f *failingModelClient) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []ToolDefinition,
) (ModelClientResult, error) {
	_ = ctx
	_ = model
	_ = messages
	_ = tools
	return ModelClientResult{}, f.err
}

type fakeConversationStore struct {
	loadMessages         []conversation.Message
	coreMemory           CoreMemory
	workingMemory        WorkingMemory
	skillCatalog         SkillCatalog
	lastUserTimestamp    time.Time
	hasLastUserTimestamp bool
	appendCalls          int
	lastAppend           []conversation.Message
}

func (f *fakeConversationStore) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	_ = ctx
	_ = turns
	return copyMessages(f.loadMessages), nil
}

func (f *fakeConversationStore) AppendTurn(
	ctx context.Context,
	messages []conversation.Message,
) error {
	_ = ctx
	f.appendCalls++
	f.lastAppend = copyMessages(messages)
	for _, msg := range messages {
		if timestamp, ok := conversation.UserMessageTimeLocal(msg); ok {
			f.lastUserTimestamp = timestamp
			f.hasLastUserTimestamp = true
		}
	}
	return nil
}

func (f *fakeConversationStore) LoadLastUserTimestamp(
	context.Context,
) (time.Time, bool, error) {
	return f.lastUserTimestamp, f.hasLastUserTimestamp, nil
}

func (f *fakeConversationStore) LoadCoreMemory(ctx context.Context) (CoreMemory, error) {
	_ = ctx
	return f.coreMemory, nil
}

func (f *fakeConversationStore) LoadWorkingMemory(ctx context.Context) (WorkingMemory, error) {
	_ = ctx
	return f.workingMemory, nil
}

func (f *fakeConversationStore) LoadSkillCatalog(ctx context.Context) (SkillCatalog, error) {
	_ = ctx
	return f.skillCatalog, nil
}

func assistantResult(text string) ModelClientResult {
	if strings.TrimSpace(text) == "" {
		return ModelClientResult{}
	}
	return ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text(text, "")),
		},
	}
}

func toolCallResult(id, name, arguments string) ModelClientResult {
	return ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.ToolCall(id, name, arguments)),
		},
		FinishReason: "tool_calls",
	}
}

func messageText(msg conversation.Message) string {
	return conversation.TextValue(msg)
}

func firstPart(msg conversation.Message) conversation.Part {
	if len(msg.Parts) == 0 {
		return conversation.Part{}
	}
	return msg.Parts[0]
}

func userTextMessage(text string) conversation.Message {
	return conversation.UserMessage(text)
}

func TestDefaultSystemPromptUsesStructuredPromptSections(t *testing.T) {
	for _, want := range []string{
		"<identity>",
		"<autonomy_and_persistence>",
		"<execution_contract>",
		"<core_memory_contract>",
		"<working_memory_contract>",
		"Prefer doing the work over announcing intent",
		"Do not present intent, plans, or assumptions as completed work",
	} {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Fatalf("DefaultSystemPrompt missing %q:\n%s", want, DefaultSystemPrompt)
		}
	}
}

func TestDefaultSystemPromptForNameIncludesNameAndIdentityBlock(t *testing.T) {
	prompt := DefaultSystemPromptForName("Jared")
	if !strings.Contains(prompt, "<identity>") {
		t.Fatalf("named prompt missing identity block:\n%s", prompt)
	}
	if !strings.Contains(prompt, "You are Jared, a pragmatic software assistant.") {
		t.Fatalf("named prompt missing agent name:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<verification_loop>") {
		t.Fatalf("named prompt missing verification block:\n%s", prompt)
	}
}

func TestDefaultSystemPromptForNamePanicsOnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("DefaultSystemPromptForName should panic on empty agent name")
		}
	}()
	_ = DefaultSystemPromptForName("   ")
}

func TestLoopReply_LoadsRecentAndPersistsTurn(t *testing.T) {
	store := &fakeConversationStore{
		loadMessages: []conversation.Message{
			conversation.UserMessage("old-question"),
			conversation.AssistantMessage(conversation.Text("old-answer", "")),
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("new-answer")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 5)

	out, err := loop.Reply(context.Background(), userTextMessage("new-question"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "new-answer" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "new-answer")
	}
	if len(out.MediaRefs) != 0 {
		t.Fatalf("Reply().MediaRefs = %#v, want empty", out.MediaRefs)
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}

	gotModelInput := model.callMsgs[0]
	if len(gotModelInput) != 4 {
		t.Fatalf("model input len = %d, want 4", len(gotModelInput))
	}
	if gotModelInput[0].Role != conversation.SystemRole {
		t.Fatalf("model input[0].Role = %q, want system", gotModelInput[0].Role)
	}
	if messageText(gotModelInput[1]) != "old-question" ||
		messageText(gotModelInput[2]) != "old-answer" {
		t.Fatalf("model input missing recent history: %#v", gotModelInput)
	}
	if messageText(gotModelInput[3]) != "new-question" {
		t.Fatalf("model input current user = %q, want new-question", messageText(gotModelInput[3]))
	}

	if store.appendCalls != 1 {
		t.Fatalf("AppendTurn calls = %d, want 1", store.appendCalls)
	}
	if len(store.lastAppend) != 2 {
		t.Fatalf("persisted turn len = %d, want 2", len(store.lastAppend))
	}
	if store.lastAppend[0].Role != conversation.UserRole ||
		messageText(store.lastAppend[0]) != "new-question" {
		t.Fatalf("persisted user message = %#v", store.lastAppend[0])
	}
	if store.lastAppend[1].Role != conversation.AssistantRole ||
		messageText(store.lastAppend[1]) != "new-answer" {
		t.Fatalf("persisted assistant message = %#v", store.lastAppend[1])
	}
}

func TestLoopReply_ComputesUserTemporalMetadataFromLastUserTimestamp(t *testing.T) {
	location := time.FixedZone("UTC+2", 2*60*60)
	lastTimestamp := time.Date(2026, time.April, 12, 9, 0, 0, 0, location)
	currentTimestamp := lastTimestamp.Add(3*time.Minute + 42*time.Second)

	store := &fakeConversationStore{
		lastUserTimestamp:    lastTimestamp,
		hasLastUserTimestamp: true,
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("new-answer")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 5)
	userMessage := conversation.Message{
		Role:  conversation.UserRole,
		Parts: []conversation.Part{conversation.Text("new-question", "")},
		UserTemporal: &conversation.UserTemporalMetadata{
			TimeLocal: currentTimestamp,
		},
	}

	if _, err := loop.Reply(context.Background(), userMessage, nil); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}

	persistedUser := store.lastAppend[0]
	if got, ok := conversation.UserMessageTimeLocal(persistedUser); !ok ||
		!got.Equal(currentTimestamp) {
		t.Fatalf("persisted user timestamp = %v, %t, want %v", got, ok, currentTimestamp)
	}
	gap, ok := conversation.SincePrevUserMessage(persistedUser)
	if !ok {
		t.Fatalf("persisted user gap missing: %#v", persistedUser.UserTemporal)
	}
	if want := 3*time.Minute + 42*time.Second; gap != want {
		t.Fatalf("persisted user gap = %s, want %s", gap, want)
	}
}

func TestLoopReply_PersistsToolCallFlow(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "echo", `{"value":"x"}`),
			assistantResult("final"),
		},
	}

	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "tool-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)

	out, err := loop.Reply(context.Background(), userTextMessage("question"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "final" {
		t.Fatalf("Reply().Text = %q, want final", out.Text)
	}
	if len(store.lastAppend) != 4 {
		t.Fatalf("persisted turn len = %d, want 4", len(store.lastAppend))
	}

	assistantCalls := conversation.ToolCalls([]conversation.Message{store.lastAppend[1]})
	if store.lastAppend[1].Role != conversation.AssistantRole || len(assistantCalls) != 1 {
		t.Fatalf("persisted assistant tool call message = %#v", store.lastAppend[1])
	}

	toolPart := firstPart(store.lastAppend[2])
	if store.lastAppend[2].Role != conversation.ToolRole ||
		toolPart.Type != conversation.ToolResultPartType ||
		toolPart.Content != "tool-output" ||
		toolPart.IsError {
		t.Fatalf("persisted tool message = %#v", store.lastAppend[2])
	}
	if store.lastAppend[3].Role != conversation.AssistantRole ||
		messageText(store.lastAppend[3]) != "final" {
		t.Fatalf("persisted final assistant message = %#v", store.lastAppend[3])
	}
}

func TestLoopReply_PersistsToolErrorsAsErrorResults(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "echo", `{"value":"x"}`),
			assistantResult("final"),
		},
	}

	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "", errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	if _, err := loop.Reply(context.Background(), userTextMessage("question"), nil); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}

	toolPart := firstPart(store.lastAppend[2])
	if toolPart.Type != conversation.ToolResultPartType || !toolPart.IsError {
		t.Fatalf("tool result part = %#v, want tool_result with is_error", toolPart)
	}
	if !strings.Contains(toolPart.Content, "tool error: boom") {
		t.Fatalf("tool error content = %q", toolPart.Content)
	}
}

func TestLoopReply_PersistsAssistantDisposition(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.Text("working", conversation.TextDispositionCommentary),
						conversation.ToolCall("call-1", "echo", `{"value":"x"}`),
					),
				},
			},
			assistantResult("done"),
		},
	}

	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "tool-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	if _, err := loop.Reply(context.Background(), userTextMessage("say hi"), nil); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if len(store.lastAppend) != 4 {
		t.Fatalf("persisted turn len = %d, want 4", len(store.lastAppend))
	}
	if got := firstPart(store.lastAppend[1]).Disposition; got != conversation.TextDispositionCommentary {
		t.Fatalf(
			"persisted assistant disposition = %q, want %q",
			got,
			conversation.TextDispositionCommentary,
		)
	}
}

func TestLoopReply_PrefersFinalDispositionOverCommentary(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.Text("thinking", conversation.TextDispositionCommentary),
					),
					conversation.AssistantMessage(
						conversation.Text("final answer", conversation.TextDispositionFinal),
					),
				},
			},
		},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("say hi"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "final answer" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "final answer")
	}
}

func TestLoopReply_DoesNotAppendGenericToolSteeringPromptWhenToolsEnabled(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("done")},
	}

	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "tool-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(
		context.Background(),
		userTextMessage("please check the workspace"),
		nil,
	)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "done")
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}
	last := model.callMsgs[0][len(model.callMsgs[0])-1]
	if last.Role != conversation.UserRole || messageText(last) != "please check the workspace" {
		t.Fatalf("last model message = %#v, want user input as last message", last)
	}
	if len(store.lastAppend) != 2 {
		t.Fatalf("persisted turn len = %d, want 2", len(store.lastAppend))
	}
	for i, msg := range model.callMsgs[0] {
		if msg.Role == conversation.SystemRole &&
			strings.Contains(
				messageText(msg),
				"call the relevant tool(s) immediately instead of narrating intent",
			) {
			t.Fatalf("model input should not include removed steering prompt, found at index %d", i)
		}
	}
}

func TestLoopReply_EmitsProgressEventsInSuccessOrder(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "read_file", `{"path":"/workspace/README.md"}`),
			assistantResult("done"),
		},
	}

	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "read_file"},
		run: func(context.Context, string) (string, error) {
			return "Path: /workspace/README.md\n--- CONTENT ---\nhello", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)

	var got []RunEvent
	out, err := loop.Reply(
		context.Background(),
		userTextMessage("read the readme"),
		RunObserverFunc(func(_ context.Context, event RunEvent) {
			got = append(got, event)
		}),
	)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want done", out.Text)
	}

	wantTypes := []RunEventType{
		RunEventRunStarted,
		RunEventModelTurnStarted,
		RunEventToolStarted,
		RunEventToolFinished,
		RunEventModelTurnStarted,
		RunEventRunFinished,
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("event len = %d, want %d", len(got), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got[i].Type != want {
			t.Fatalf("event[%d].Type = %q, want %q", i, got[i].Type, want)
		}
	}
	if got[2].ToolCall.Name != "read_file" {
		t.Fatalf("tool started name = %q, want read_file", got[2].ToolCall.Name)
	}
	if got[3].ToolOutput == "" {
		t.Fatal("tool finished output should not be empty")
	}
	if got[5].FinalText != "done" {
		t.Fatalf("run finished final text = %q, want done", got[5].FinalText)
	}
}

func TestLoopReply_EmitsRunFailedOnModelError(t *testing.T) {
	loop := NewLoop(
		&failingModelClient{err: errors.New("boom")},
		nil,
		[]string{"m1"},
		DefaultSystemPrompt,
		nil,
		3,
	)

	var got []RunEvent
	_, err := loop.Reply(
		context.Background(),
		userTextMessage("hello"),
		RunObserverFunc(func(_ context.Context, event RunEvent) {
			got = append(got, event)
		}),
	)
	if err == nil {
		t.Fatal("Reply() error = nil, want non-nil")
	}

	wantTypes := []RunEventType{
		RunEventRunStarted,
		RunEventModelTurnStarted,
		RunEventRunFailed,
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("event len = %d, want %d", len(got), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got[i].Type != want {
			t.Fatalf("event[%d].Type = %q, want %q", i, got[i].Type, want)
		}
	}
	if got[2].Err == nil {
		t.Fatal("run failed error should not be nil")
	}
}

func TestLoopReply_UsesEligibleFallbackCandidatesOnly(t *testing.T) {
	store := &fakeConversationStore{}
	planner := &fakePlanner{
		plan: func(modelRefs []string, requirements modelselection.Requirements) (modelselection.Plan, error) {
			if len(modelRefs) != 2 || modelRefs[0] != "m1" || modelRefs[1] != "m2" {
				t.Fatalf("planned model refs = %#v, want [m1 m2]", modelRefs)
			}
			if !requirements.Text || requirements.ImageInput || requirements.ToolCalling {
				t.Fatalf("requirements = %#v, want text-only", requirements)
			}
			return modelselection.Plan{
				EligibleRefs: []string{"m2"},
				Skipped: []modelselection.Skip{
					{Ref: "m1", Reason: "missing capabilities [text]"},
				},
			}, nil
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("done")},
	}

	loop := NewLoopWithPlanner(
		model,
		planner,
		nil,
		[]string{"m1", "m2"},
		DefaultSystemPrompt,
		store,
		3,
	)
	out, err := loop.Reply(context.Background(), userTextMessage("hello"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "done")
	}
	if len(model.callModels) != 1 || model.callModels[0] != "m2" {
		t.Fatalf("callModels = %#v, want [m2]", model.callModels)
	}
	if len(planner.plannedRequirements) != 1 {
		t.Fatalf("plannedRequirements len = %d, want 1", len(planner.plannedRequirements))
	}
}

func TestLoopReply_FailsBeforeProviderCallsWhenNoEligibleModels(t *testing.T) {
	store := &fakeConversationStore{}
	planner := &fakePlanner{
		plan: func(_ []string, _ modelselection.Requirements) (modelselection.Plan, error) {
			return modelselection.Plan{
				Skipped: []modelselection.Skip{
					{Ref: "m1", Reason: "missing capabilities [text]"},
					{Ref: "m2", Reason: "missing capabilities [text]"},
				},
			}, nil
		},
	}
	model := &fakeModelClient{}

	loop := NewLoopWithPlanner(
		model,
		planner,
		nil,
		[]string{"m1", "m2"},
		DefaultSystemPrompt,
		store,
		3,
	)
	out, err := loop.Reply(context.Background(), userTextMessage("hello"), nil)
	if out.Text != "" {
		t.Fatalf("Reply().Text = %q, want empty", out.Text)
	}
	var noEligibleErr *modelselection.NoEligibleError
	if !errors.As(err, &noEligibleErr) {
		t.Fatalf("Reply() error = %v, want NoEligibleError", err)
	}
	if len(model.callModels) != 0 {
		t.Fatalf("callModels = %#v, want no provider calls", model.callModels)
	}
	if store.appendCalls != 0 {
		t.Fatalf("AppendTurn calls = %d, want 0", store.appendCalls)
	}
}

func TestLoopReply_ToolProducedImageRequiresImageInputOnNextTurn(t *testing.T) {
	store := &fakeConversationStore{}
	planner := &fakePlanner{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "load_image", `{"path":"artifact.png"}`),
			assistantResult("done"),
		},
	}
	registry, err := NewToolRegistry(&structuredTestTool{
		def: ToolDefinition{Name: "load_image"},
		runResult: func(context.Context, string) (ToolResult, error) {
			return ToolResult{
				Output:    "Loaded image: /workspace/artifact.png",
				MediaRefs: []string{"media://sha256/abc"},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoopWithPlanner(
		model,
		planner,
		registry,
		[]string{"m1"},
		DefaultSystemPrompt,
		store,
		3,
	)
	out, err := loop.Reply(context.Background(), userTextMessage("inspect the screenshot"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want done", out.Text)
	}
	if got := out.MediaRefs; len(got) != 1 || got[0] != "media://sha256/abc" {
		t.Fatalf("Reply().MediaRefs = %#v, want tool image ref", got)
	}
	if got := out.Attachments; len(got) != 1 || got[0].MediaRef != "media://sha256/abc" {
		t.Fatalf("Reply().Attachments = %#v, want image attachment", got)
	}
	if len(store.lastAppend) != 4 {
		t.Fatalf("persisted turn len = %d, want 4", len(store.lastAppend))
	}
	if toolParts := store.lastAppend[2].Parts; len(toolParts) != 1 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("persisted tool parts = %#v, want tool_result only", toolParts)
	}
	final := store.lastAppend[3]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 2 {
		t.Fatalf("persisted final assistant = %#v, want text plus image", final)
	}
	if final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("persisted final image part = %#v, want promoted image", final.Parts[1])
	}
	if len(planner.plannedRequirements) != 2 {
		t.Fatalf("plannedRequirements len = %d, want 2", len(planner.plannedRequirements))
	}
	if planner.plannedRequirements[0].ImageInput {
		t.Fatalf("first turn requirements = %#v, want text-only", planner.plannedRequirements[0])
	}
	if !planner.plannedRequirements[1].ImageInput {
		t.Fatalf("second turn requirements = %#v, want image_input", planner.plannedRequirements[1])
	}
}

func TestLoopReply_ExecThenLoadImageCarriesFinalMediaRefs(t *testing.T) {
	store := &fakeConversationStore{}
	planner := &fakePlanner{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult(
				"call-1",
				"exec",
				`{"command":"curl -o /workspace/cat.png https://example.com/cat.png"}`,
			),
			toolCallResult("call-2", "load_image", `{"path":"/workspace/cat.png"}`),
			assistantResult("done"),
		},
	}
	registry, err := NewToolRegistry(
		&testTool{
			def: ToolDefinition{Name: "exec"},
			run: func(context.Context, string) (string, error) {
				return "Exit-Code: 0\n--- STDOUT ---\ndownloaded /workspace/cat.png", nil
			},
		},
		&structuredTestTool{
			def: ToolDefinition{Name: "load_image"},
			runResult: func(context.Context, string) (ToolResult, error) {
				return ToolResult{
					Output:    "Loaded image: /workspace/cat.png",
					MediaRefs: []string{"media://sha256/cat"},
				}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoopWithPlanner(
		model,
		planner,
		registry,
		[]string{"m1"},
		DefaultSystemPrompt,
		store,
		3,
	)
	out, err := loop.Reply(
		context.Background(),
		userTextMessage("download and send the image"),
		nil,
	)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want done", out.Text)
	}
	if got := out.MediaRefs; len(got) != 1 || got[0] != "media://sha256/cat" {
		t.Fatalf("Reply().MediaRefs = %#v, want load_image ref", got)
	}
	if got := out.Attachments; len(got) != 1 || got[0].MediaRef != "media://sha256/cat" {
		t.Fatalf("Reply().Attachments = %#v, want load_image attachment", got)
	}
	if len(store.lastAppend) != 6 {
		t.Fatalf("persisted turn len = %d, want 6", len(store.lastAppend))
	}
	if toolParts := store.lastAppend[4].Parts; len(toolParts) != 1 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("persisted load_image tool parts = %#v, want tool_result only", toolParts)
	}
	final := store.lastAppend[5]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 2 {
		t.Fatalf("persisted final assistant = %#v, want text plus image", final)
	}
	if final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/cat" {
		t.Fatalf("persisted final image part = %#v, want promoted image", final.Parts[1])
	}
	if len(planner.plannedRequirements) != 3 {
		t.Fatalf("plannedRequirements len = %d, want 3", len(planner.plannedRequirements))
	}
	if planner.plannedRequirements[0].ImageInput {
		t.Fatalf("first turn requirements = %#v, want text-only", planner.plannedRequirements[0])
	}
	if planner.plannedRequirements[1].ImageInput {
		t.Fatalf(
			"second turn requirements = %#v, want text-only after exec",
			planner.plannedRequirements[1],
		)
	}
	if !planner.plannedRequirements[2].ImageInput {
		t.Fatalf(
			"third turn requirements = %#v, want image_input after load_image",
			planner.plannedRequirements[2],
		)
	}
}

func TestLoopReply_FallsBackAcrossEligibleModelsInOrder(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		complete: func(
			_ context.Context,
			model string,
			_ []conversation.Message,
			_ []ToolDefinition,
		) (ModelClientResult, error) {
			if model == "m1" {
				return ModelClientResult{}, errors.New("boom")
			}
			return assistantResult("done"), nil
		},
	}

	loop := NewLoop(model, nil, []string{"m1", "m2"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("hello"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "done")
	}
	if len(model.callModels) != 2 || model.callModels[0] != "m1" || model.callModels[1] != "m2" {
		t.Fatalf("callModels = %#v, want [m1 m2]", model.callModels)
	}
}

func TestLoopReply_DoesNotAppendSteeringPromptWithoutTools(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("plain")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("what is 2 + 2?"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "plain" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "plain")
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}
	last := model.callMsgs[0][len(model.callMsgs[0])-1]
	if last.Role != conversation.UserRole {
		t.Fatalf(
			"last model message role = %q, want %q",
			last.Role,
			conversation.UserRole,
		)
	}
}

func TestLoopReply_RetriesOnceOnEmptyAssistantResponse(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			{},
			assistantResult("after-retry"),
		},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("answer me"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "after-retry" {
		t.Fatalf("Reply().Text = %q, want %q", out.Text, "after-retry")
	}
	if len(model.callMsgs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.callMsgs))
	}
	second := model.callMsgs[1]
	if len(second) < 1 {
		t.Fatalf("second call should include messages")
	}
	last := second[len(second)-1]
	if last.Role != conversation.SystemRole ||
		messageText(last) != emptyResponseRetrySteeringPrompt {
		t.Fatalf("second call last message = %#v, want empty-response retry steering prompt", last)
	}
	if len(store.lastAppend) != 2 {
		t.Fatalf("persisted turn len = %d, want 2", len(store.lastAppend))
	}
	if messageText(store.lastAppend[1]) != "after-retry" {
		t.Fatalf("persisted assistant message = %#v", store.lastAppend[1])
	}
}

func TestLoopReply_ReturnsNoTextAfterEmptyRetryExhausted(t *testing.T) {
	store := &fakeConversationStore{}
	emptyResults := make([]ModelClientResult, 0, maxEmptyAssistantRetries+1)
	for i := 0; i < maxEmptyAssistantRetries+1; i++ {
		emptyResults = append(emptyResults, ModelClientResult{})
	}
	model := &fakeModelClient{
		results: emptyResults,
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("still there?"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "(assistant returned no text)" {
		t.Fatalf(
			"Reply().Text = %q, want %q",
			out.Text,
			"(assistant returned no text)",
		)
	}
	if len(model.callMsgs) != maxEmptyAssistantRetries+1 {
		t.Fatalf("model calls = %d, want %d", len(model.callMsgs), maxEmptyAssistantRetries+1)
	}
	if len(store.lastAppend) != 1 {
		t.Fatalf("persisted turn len = %d, want 1", len(store.lastAppend))
	}
	if store.lastAppend[0].Role != conversation.UserRole ||
		messageText(store.lastAppend[0]) != "still there?" {
		t.Fatalf("persisted turn = %#v", store.lastAppend)
	}
}

func TestLoopReply_IncludesCoreMemoryInSeparateSystemMessage(t *testing.T) {
	store := &fakeConversationStore{
		coreMemory: CoreMemory{
			Files: []CoreMemoryFile{
				{
					RelativePath: "core/AGENT.md",
					Description:  "Core behavior guide",
					Limit:        6000,
					Content:      "# AGENT.md\n- Be precise.",
				},
			},
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("ok")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)

	_, err := loop.Reply(context.Background(), userTextMessage("hello"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}

	if got, want := len(model.callMsgs[0]), 3; got != want {
		t.Fatalf("request messages len = %d, want %d", got, want)
	}

	system := messageText(model.callMsgs[0][1])
	if !strings.Contains(system, "<core_memory>") {
		t.Fatalf("core memory message missing section: %q", system)
	}
	if !strings.Contains(
		system,
		"<core_file description=\"Core behavior guide\" limit=\"6000\" path=\"core/AGENT.md\">",
	) {
		t.Fatalf("core memory message missing core file metadata: %q", system)
	}
	if !strings.Contains(system, "# AGENT.md\n- Be precise.") {
		t.Fatalf("core memory message missing core file body: %q", system)
	}
}

func TestLoopReply_IncludesOrderedSystemPrefix(t *testing.T) {
	store := &fakeConversationStore{
		coreMemory: CoreMemory{
			Files: []CoreMemoryFile{
				{
					RelativePath: "core/AGENT.md",
					Content:      "# AGENT.md\n- Be precise.",
				},
			},
		},
		workingMemory: WorkingMemory{
			RelativePath: "working/WORKING_MEMORY.md",
			Content: strings.Join([]string{
				"# Working Memory",
				"",
				"## Active Tasks",
				"- Implement canonical working memory",
			}, "\n"),
		},
		skillCatalog: SkillCatalog{
			Entries: []SkillCatalogEntry{
				{
					Name:        "skill-creator",
					Description: "Create or update skills.",
				},
			},
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("ok")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	if _, err := loop.Reply(context.Background(), userTextMessage("hello"), nil); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}

	call := model.callMsgs[0]
	if got, want := len(call), 5; got != want {
		t.Fatalf("request messages len = %d, want %d", got, want)
	}
	if got := messageText(call[0]); got != DefaultSystemPrompt {
		t.Fatalf("base system message = %q, want default prompt", got)
	}
	if got := messageText(call[1]); !strings.Contains(got, "<core_memory>") {
		t.Fatalf("core memory message missing section:\n%s", got)
	}
	if got := messageText(call[2]); !strings.Contains(got, "<skill_catalog>") {
		t.Fatalf("skill catalog message missing section:\n%s", got)
	}
	working := messageText(call[3])
	for _, want := range []string{
		"<working_memory path=\"working/WORKING_MEMORY.md\">",
		"# Working Memory",
		"Implement canonical working memory",
	} {
		if !strings.Contains(working, want) {
			t.Fatalf("working memory message missing %q:\n%s", want, working)
		}
	}
	if got := call[4].Role; got != conversation.UserRole {
		t.Fatalf("call[4].Role = %q, want %q", got, conversation.UserRole)
	}
	if got := messageText(call[4]); got != "hello" {
		t.Fatalf("user message = %q, want hello", got)
	}
}

func TestLoopReply_IncludesSkillCatalogInSeparateSystemMessage(t *testing.T) {
	store := &fakeConversationStore{
		skillCatalog: SkillCatalog{
			Entries: []SkillCatalogEntry{
				{
					Name:          "skill-creator",
					Description:   "Create or update skills.",
					Source:        "builtin",
					SkillFilePath: "/skills/@builtin/skill-creator/SKILL.md",
				},
			},
			Warnings: []string{"skipping invalid shared skill \"broken\": missing SKILL.md"},
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("ok")},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)
	if _, err := loop.Reply(context.Background(), userTextMessage("hello"), nil); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}

	if got, want := len(model.callMsgs[0]), 3; got != want {
		t.Fatalf("request messages len = %d, want %d", got, want)
	}
	system := messageText(model.callMsgs[0][1])
	for _, want := range []string{
		"<skill_catalog>",
		`<skill name="skill-creator" path="/skills/@builtin/skill-creator/SKILL.md" source="builtin">`,
		"Create or update skills.",
		"<warning>",
		`skipping invalid shared skill "broken": missing SKILL.md`,
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
}

func TestNewLoop_DefaultMaxTurns(t *testing.T) {
	loop := NewLoop(
		&fakeModelClient{},
		nil,
		[]string{"m1"},
		DefaultSystemPrompt,
		&fakeConversationStore{},
		6,
	)
	if loop.maxTurns != 96 {
		t.Fatalf("loop.maxTurns = %d, want 96", loop.maxTurns)
	}
}

func TestLoopReply_AllowsMoreThanTwelveToolCallTurns(t *testing.T) {
	store := &fakeConversationStore{}

	const toolTurns = 14
	results := make([]ModelClientResult, 0, toolTurns+1)
	for i := 0; i < toolTurns; i++ {
		results = append(results, toolCallResult(
			fmt.Sprintf("call-%d", i),
			"echo",
			`{"value":"x"}`,
		))
	}
	results = append(results, assistantResult("done"))

	model := &fakeModelClient{results: results}
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "tool-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("question"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want done", out.Text)
	}
	if len(model.callMsgs) != toolTurns+1 {
		t.Fatalf("model calls = %d, want %d", len(model.callMsgs), toolTurns+1)
	}
}

func TestLoopReply_StopsAtHardLimitAndPersistsInterruptedTurn(t *testing.T) {
	store := &fakeConversationStore{}

	results := make([]ModelClientResult, 0, defaultMaxTurns)
	for i := 0; i < defaultMaxTurns; i++ {
		results = append(results, toolCallResult(
			fmt.Sprintf("call-%d", i),
			"echo",
			fmt.Sprintf(`{"step":%d}`, i),
		))
	}

	model := &fakeModelClient{results: results}
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "tool-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("question"), nil)
	if out.Text != "" {
		t.Fatalf("Reply().Text = %q, want empty", out.Text)
	}
	var stopErr *StopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("Reply() error = %v, want StopError", err)
	}
	if stopErr.Reason != StopReasonToolTurnLimit {
		t.Fatalf("stop reason = %q, want %q", stopErr.Reason, StopReasonToolTurnLimit)
	}
	if store.appendCalls != 1 {
		t.Fatalf("AppendTurn calls = %d, want 1", store.appendCalls)
	}
	if len(store.lastAppend) == 0 {
		t.Fatalf("persisted turn is empty")
	}
	last := store.lastAppend[len(store.lastAppend)-1]
	if last.Role != conversation.AssistantRole {
		t.Fatalf("last persisted role = %q, want assistant", last.Role)
	}
	if !strings.Contains(messageText(last), "reached maximum tool-call turns (96)") {
		t.Fatalf("last persisted message = %q", messageText(last))
	}
}

func TestLoopReply_StopsOnNoProgressToolLoop(t *testing.T) {
	store := &fakeConversationStore{}

	results := make([]ModelClientResult, 0, defaultToolLoopCriticalThreshold+5)
	for i := 0; i < defaultToolLoopCriticalThreshold+5; i++ {
		results = append(results, toolCallResult(
			fmt.Sprintf("call-%d", i),
			"echo",
			`{"value":"stuck"}`,
		))
	}

	model := &fakeModelClient{results: results}
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
		run: func(context.Context, string) (string, error) {
			return "same-output", nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loop := NewLoop(model, registry, []string{"m1"}, DefaultSystemPrompt, store, 3)
	out, err := loop.Reply(context.Background(), userTextMessage("question"), nil)
	if out.Text != "" {
		t.Fatalf("Reply().Text = %q, want empty", out.Text)
	}
	var stopErr *StopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("Reply() error = %v, want StopError", err)
	}
	if stopErr.Reason != StopReasonToolLoopDetected {
		t.Fatalf("stop reason = %q, want %q", stopErr.Reason, StopReasonToolLoopDetected)
	}
	if len(model.callMsgs) >= defaultMaxTurns {
		t.Fatalf("expected early stop before hard limit, model calls = %d", len(model.callMsgs))
	}
	if store.appendCalls != 1 {
		t.Fatalf("AppendTurn calls = %d, want 1", store.appendCalls)
	}
	last := store.lastAppend[len(store.lastAppend)-1]
	if !strings.Contains(messageText(last), "detected repeated tool-call loop") {
		t.Fatalf("last persisted message = %q", messageText(last))
	}
}
