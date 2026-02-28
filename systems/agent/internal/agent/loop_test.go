package agent

import (
	"context"
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
