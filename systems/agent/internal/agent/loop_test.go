package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeModelClient struct {
	results  []ModelClientResult
	callMsgs [][]Message
}

func (f *fakeModelClient) Complete(
	ctx context.Context,
	model string,
	messages []Message,
	tools []ToolDefinition,
) (ModelClientResult, error) {
	_ = ctx
	_ = model
	_ = tools
	f.callMsgs = append(f.callMsgs, copyMessages(messages))
	if len(f.results) == 0 {
		return ModelClientResult{Content: "ok"}, nil
	}
	out := f.results[0]
	f.results = f.results[1:]
	return out, nil
}

type fakeConversationStore struct {
	loadMessages []Message
	coreMemory   CoreMemory
	appendCalls  int
	lastAppend   []Message
}

func (f *fakeConversationStore) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]Message, error) {
	_ = ctx
	_ = turns
	return copyMessages(f.loadMessages), nil
}

func (f *fakeConversationStore) AppendTurn(ctx context.Context, messages []Message) error {
	_ = ctx
	f.appendCalls++
	f.lastAppend = copyMessages(messages)
	return nil
}

func (f *fakeConversationStore) LoadCoreMemory(ctx context.Context) (CoreMemory, error) {
	_ = ctx
	return f.coreMemory, nil
}

func TestLoopReply_LoadsRecentAndPersistsTurn(t *testing.T) {
	store := &fakeConversationStore{
		loadMessages: []Message{
			{Role: UserRole, Content: "old-question"},
			{Role: AssistantRole, Content: "old-answer"},
		},
	}
	model := &fakeModelClient{
		results: []ModelClientResult{
			{Content: "new-answer"},
		},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 5)

	out, err := loop.Reply(context.Background(), "new-question")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out != "new-answer" {
		t.Fatalf("Reply() = %q, want %q", out, "new-answer")
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}

	gotModelInput := model.callMsgs[0]
	if len(gotModelInput) != 4 {
		t.Fatalf("model input len = %d, want 4", len(gotModelInput))
	}
	if gotModelInput[0].Role != SystemRole {
		t.Fatalf("model input[0].Role = %q, want system", gotModelInput[0].Role)
	}
	if gotModelInput[1].Content != "old-question" || gotModelInput[2].Content != "old-answer" {
		t.Fatalf("model input missing recent history: %#v", gotModelInput)
	}
	if gotModelInput[3].Content != "new-question" {
		t.Fatalf("model input current user = %q, want new-question", gotModelInput[3].Content)
	}

	if store.appendCalls != 1 {
		t.Fatalf("AppendTurn calls = %d, want 1", store.appendCalls)
	}
	if len(store.lastAppend) != 2 {
		t.Fatalf("persisted turn len = %d, want 2", len(store.lastAppend))
	}
	if store.lastAppend[0].Role != UserRole || store.lastAppend[0].Content != "new-question" {
		t.Fatalf("persisted user message = %#v", store.lastAppend[0])
	}
	if store.lastAppend[1].Role != AssistantRole || store.lastAppend[1].Content != "new-answer" {
		t.Fatalf("persisted assistant message = %#v", store.lastAppend[1])
	}
}

func TestLoopReply_PersistsToolCallFlow(t *testing.T) {
	store := &fakeConversationStore{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			{
				ToolCalls: []ToolCall{
					{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`},
				},
			},
			{Content: "final"},
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

	out, err := loop.Reply(context.Background(), "question")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out != "final" {
		t.Fatalf("Reply() = %q, want final", out)
	}
	if len(store.lastAppend) != 4 {
		t.Fatalf("persisted turn len = %d, want 4", len(store.lastAppend))
	}
	if store.lastAppend[1].Role != AssistantRole || len(store.lastAppend[1].ToolCalls) != 1 {
		t.Fatalf("persisted assistant tool call message = %#v", store.lastAppend[1])
	}
	if store.lastAppend[2].Role != ToolRole || store.lastAppend[2].Content != "tool-output" {
		t.Fatalf("persisted tool message = %#v", store.lastAppend[2])
	}
	if store.lastAppend[3].Role != AssistantRole || store.lastAppend[3].Content != "final" {
		t.Fatalf("persisted final assistant message = %#v", store.lastAppend[3])
	}
}

func TestLoopReply_IncludesCoreMemoryInSystemMessage(t *testing.T) {
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
		results: []ModelClientResult{
			{Content: "ok"},
		},
	}

	loop := NewLoop(model, nil, []string{"m1"}, DefaultSystemPrompt, store, 3)

	_, err := loop.Reply(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}

	system := model.callMsgs[0][0].Content
	if !strings.Contains(system, "Core Memory (persistent; always in-context):") {
		t.Fatalf("system prompt missing core memory header: %q", system)
	}
	if !strings.Contains(
		system,
		"<core_file path=\"core/AGENT.md\" description=\"Core behavior guide\" limit=\"6000\">",
	) {
		t.Fatalf("system prompt missing core file metadata: %q", system)
	}
	if !strings.Contains(system, "# AGENT.md\n- Be precise.") {
		t.Fatalf("system prompt missing core file body: %q", system)
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
		results = append(results, ModelClientResult{
			ToolCalls: []ToolCall{
				{
					ID:        fmt.Sprintf("call-%d", i),
					Name:      "echo",
					Arguments: `{"value":"x"}`,
				},
			},
		})
	}
	results = append(results, ModelClientResult{Content: "done"})

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
	out, err := loop.Reply(context.Background(), "question")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out != "done" {
		t.Fatalf("Reply() = %q, want done", out)
	}
	if len(model.callMsgs) != toolTurns+1 {
		t.Fatalf("model calls = %d, want %d", len(model.callMsgs), toolTurns+1)
	}
}

func TestLoopReply_StopsAtHardLimitAndPersistsInterruptedTurn(t *testing.T) {
	store := &fakeConversationStore{}

	results := make([]ModelClientResult, 0, defaultMaxTurns)
	for i := 0; i < defaultMaxTurns; i++ {
		results = append(results, ModelClientResult{
			ToolCalls: []ToolCall{
				{
					ID:        fmt.Sprintf("call-%d", i),
					Name:      "echo",
					Arguments: fmt.Sprintf(`{"step":%d}`, i),
				},
			},
		})
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
	out, err := loop.Reply(context.Background(), "question")
	if out != "" {
		t.Fatalf("Reply() output = %q, want empty", out)
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
	if last.Role != AssistantRole {
		t.Fatalf("last persisted role = %q, want assistant", last.Role)
	}
	if !strings.Contains(last.Content, "reached maximum tool-call turns (96)") {
		t.Fatalf("last persisted message = %q", last.Content)
	}
}

func TestLoopReply_StopsOnNoProgressToolLoop(t *testing.T) {
	store := &fakeConversationStore{}

	results := make([]ModelClientResult, 0, defaultToolLoopCriticalThreshold+5)
	for i := 0; i < defaultToolLoopCriticalThreshold+5; i++ {
		results = append(results, ModelClientResult{
			ToolCalls: []ToolCall{
				{
					ID:        fmt.Sprintf("call-%d", i),
					Name:      "echo",
					Arguments: `{"value":"stuck"}`,
				},
			},
		})
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
	out, err := loop.Reply(context.Background(), "question")
	if out != "" {
		t.Fatalf("Reply() output = %q, want empty", out)
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
	if !strings.Contains(last.Content, "detected repeated tool-call loop") {
		t.Fatalf("last persisted message = %q", last.Content)
	}
}
