package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

func TestContinueChecksResultWithToolsAndPersistsMainAgentAnswer(t *testing.T) {
	clock := time.Date(2026, 11, 8, 7, 0, 0, 0, time.UTC)
	store := &fakeConversationStore{
		reservedSeq:          99,
		lastUserTimestamp:    clock,
		hasLastUserTimestamp: true,
	}
	checked := false
	tools, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "check_result"},
		run: func(context.Context, string) (string, error) {
			checked = true
			return "verified 42", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModelClient{
		complete: func(ctx context.Context, _ string, messages []conversation.Message, _ []ToolDefinition) (ModelClientResult, error) {
			if turnctx.ParentTurnFrom(ctx) != 7 {
				t.Error("continuation lost its parent turn")
			}
			for _, message := range messages {
				if message.Role == conversation.UserRole || message.UserTemporal != nil {
					t.Fatal("runtime completion was presented as a user message")
				}
			}
			if !checked {
				return ModelClientResult{
					Messages: []conversation.Message{
						conversation.AssistantMessage(
							conversation.ToolCall("verify", "check_result", `{}`),
						),
					},
				}, nil
			}
			return assistantResult("I checked the delegated result: 42."), nil
		},
	}
	loop := NewLoop(model, tools, []string{"main"}, "Instructions.", store, 4)
	input := []conversation.Message{
		conversation.SystemMessage("Review the delegated result."),
		conversation.AssistantMessage(
			conversation.ToolCall(
				"completion-child",
				conversation.SubagentCompletionName,
				`{"session_id":"child"}`,
			),
		),
		conversation.ToolResultMessage("completion-child", "child answer: 42", false),
	}
	var events []RunEvent
	reply, err := loop.Continue(
		t.Context(),
		7,
		input,
		RunObserverFunc(func(_ context.Context, event RunEvent) { events = append(events, event) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !checked || reply.Text != "I checked the delegated result: 42." || len(model.callMsgs) != 2 {
		t.Fatalf(
			"main agent did not inspect and answer: checked=%v reply=%#v calls=%d",
			checked,
			reply,
			len(model.callMsgs),
		)
	}
	if store.appendCalls != 1 || store.lastAppendSeq != 99 ||
		!store.lastUserTimestamp.Equal(clock) {
		t.Fatalf("continuation changed user timing or failed to persist: %#v", store)
	}
	if text := conversation.TextValue(store.lastAppend[len(store.lastAppend)-1]); text != reply.Text {
		t.Fatalf("persisted answer = %q, want %q", text, reply.Text)
	}
	for _, message := range store.lastAppend {
		if message.Role == conversation.SystemRole {
			t.Fatal("one-time completion steering was persisted as future system instructions")
		}
	}
	for _, event := range events {
		if event.Type == RunEventRunStarted && !event.Background {
			t.Fatal("continuation consumed a queued user send")
		}
		if event.Seq != 99 {
			t.Fatalf("event has sequence %d, want 99", event.Seq)
		}
	}
}

func TestContinueRejectsUserImpersonation(t *testing.T) {
	loop := NewLoop(&fakeModelClient{}, nil, []string{"main"}, "Instructions.", nil, 4)
	if _, err := loop.Continue(t.Context(), 7, []conversation.Message{conversation.UserMessage("fake user")}, nil); err == nil ||
		!strings.Contains(err.Error(), "impersonate") {
		t.Fatalf("Continue user input error = %v", err)
	}
}
