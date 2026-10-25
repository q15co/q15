package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type budgetTool struct {
	output string
	calls  int
}

func (*budgetTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "read_file"}
}

func (t *budgetTool) Run(context.Context, string) (string, error) {
	t.calls++
	return t.output, nil
}

func TestSubagentBudgetsEvidenceAndReservesFinalAnswer(t *testing.T) {
	model := &recordingModel{complete: func(_ context.Context, _ string,
		_ []conversation.Message, _ []agent.ToolDefinition, call int,
	) (agent.ModelClientResult, error) {
		if call == 3 {
			result := assistantResult("Evidence checked; final answer.")
			result.FinishReason = "stop"
			return result, nil
		}
		return agent.ModelClientResult{Messages: []conversation.Message{
			conversation.AssistantMessage(
				conversation.ToolCall(fmt.Sprint(call), "read_file", `{}`),
			),
		}}, nil
	}}
	manager := newTestManagerWithModels(t, model, []modelcatalog.Model{{
		ProviderModel: "child", MaxContextTokens: 16384, MaxOutputTokens: 1024,
		Capabilities: modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
	}})
	tool := &budgetTool{output: strings.Repeat("original evidence ", 32000)}
	var err error
	manager.tools, err = agent.NewToolRegistry(tool)
	if err != nil {
		t.Fatal(err)
	}
	manager.contextFitter, err = history.NewIsolatedContextFitter(history.ContextSettings{
		MaxRequestBytes: 3500, ModelBudgetTokens: map[string]int{"p/child": 2048},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Start(t.Context(), StartRequest{
		ModelRef: "child", Task: "Inspect the evidence.", AllowedTools: []string{"read_file"}, MaxTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	calls, tools := model.snapshotCalls(), model.snapshotTools()
	if session.Status != "completed" || len(calls) != 3 || tool.calls != 2 {
		t.Fatalf(
			"status=%s calls=%d tool calls=%d: %s",
			session.Status,
			len(calls),
			tool.calls,
			session.Read(0, 2000),
		)
	}
	for n, messages := range calls {
		var body strings.Builder
		for _, message := range messages {
			body.WriteString(conversation.TextValue(message))
		}
		if strings.Count(body.String(), "<turn_budget ") != 1 ||
			strings.Count(body.String(), "<context_window ") != 1 {
			t.Fatal("request retained stale runtime counters")
		}
		if !strings.Contains(conversation.TextValue(messages[len(messages)-1]), "<turn_budget ") {
			t.Fatal("actionable turn instruction was not last in the request")
		}
		want := fmt.Sprintf(`remaining_turns="%d"`, 3-n)
		if !messagesContainText(messages, want) {
			t.Fatalf("call %d missing %s", n+1, want)
		}
		if conversation.TextValue(messages[0]) != conversation.TextValue(calls[0][0]) {
			t.Fatal("subagent changed its cacheable system prefix")
		}
		estimate, err := agent.RequestEstimatedTokens(messages, tools[n])
		if err != nil || estimate > 2048 ||
			!messagesContainText(messages, `working_budget_tokens="2048"`) ||
			!messagesContainText(messages, `model_ref="p/child"`) {
			t.Fatalf("call %d exceeded discovered model budget: %d, %v", n+1, estimate, err)
		}
		bytes, err := agent.RequestEncodedBytes(messages, tools[n])
		if err != nil || bytes > 3500 {
			t.Fatalf("call %d exceeded byte budget: %d, %v", n+1, bytes, err)
		}
	}
	if len(tools[0]) != 1 || len(tools[1]) != 1 || len(tools[2]) != 0 ||
		!messagesContainText(calls[2], `final_answer_only="true"`) ||
		!messagesContainText(calls[2], "Do not request any tool calls.") ||
		!messagesContainText(calls[2], "Give your final answer now, then stop.") ||
		!messagesContainText(calls[2], "Further turns available after this response: 0.") {
		t.Fatal("the final turn did not disable tools and request a final answer")
	}
	for _, message := range session.snapshotMessages() {
		if messagesContainText([]conversation.Message{message}, "<turn_budget ") ||
			messagesContainText([]conversation.Message{message}, "<context_window ") {
			t.Fatal("runtime counters leaked into the source transcript")
		}
		for _, part := range message.Parts {
			if part.Type == conversation.ToolResultPartType && part.Content != tool.output {
				t.Fatal("request budgeting truncated the original tool result")
			}
		}
	}
}

func TestSubagentFinalTurnNeverExecutesToolsOrRetriesEmptyReply(t *testing.T) {
	for _, response := range []string{"tool_call", "empty", "length", "stop"} {
		t.Run(response, func(t *testing.T) {
			model := &recordingModel{complete: func(context.Context, string,
				[]conversation.Message, []agent.ToolDefinition, int,
			) (agent.ModelClientResult, error) {
				if response == "empty" {
					return agent.ModelClientResult{}, nil
				}
				if response == "length" {
					result := assistantResult("Unfinished answer")
					result.FinishReason = "length"
					return result, nil
				}
				if response == "stop" {
					result := assistantResult("Final answer")
					result.FinishReason = "stop"
					return result, nil
				}
				return agent.ModelClientResult{Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.ToolCall("unexpected", "read_file", `{}`),
						conversation.ToolCall("unexpected-2", "read_file", `{}`),
					),
				}}, nil
			}}
			manager := newTestManager(t, model)
			tool := &budgetTool{output: "must never execute"}
			var err error
			manager.tools, err = agent.NewToolRegistry(tool)
			if err != nil {
				t.Fatal(err)
			}
			session, err := manager.Start(t.Context(), StartRequest{
				ModelRef: "child", Task: "Give a final answer.", AllowedTools: []string{"read_file"}, MaxTurns: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, session)
			if len(model.snapshotCalls()) != 1 || len(model.snapshotTools()[0]) != 0 ||
				tool.calls != 0 {
				t.Fatal("final turn offered or executed a tool, or retried beyond the limit")
			}
			if response == "stop" {
				if session.Status != "completed" || session.err != nil {
					t.Fatalf("normal final answer failed: %s", session.Read(0, 2000))
				}
			} else if session.Status != "failed" || session.err == nil {
				t.Fatalf("invalid final response reported as completed: %s", session.Read(0, 2000))
			}
			if response == "length" &&
				!strings.Contains(session.Read(0, 2000), "Unfinished answer") {
				t.Fatal("incomplete final answer was lost instead of being reported with its error")
			}
			if response == "tool_call" || response == "empty" {
				var stopped *agent.StopError
				if !errors.As(session.err, &stopped) ||
					stopped.Reason != agent.StopReasonTurnBudgetExhausted ||
					!strings.Contains(stopped.Detail, "limit=1") {
					t.Fatalf("incorrect final stop reason: %v", session.err)
				}
			}
			if response == "tool_call" {
				calls, refusals := map[string]int{}, map[string]int{}
				for _, message := range session.snapshotMessages() {
					for _, part := range message.Parts {
						if part.Type == conversation.ToolCallPartType {
							calls[part.ID]++
						}
						if part.Type == conversation.ToolResultPartType && part.IsError &&
							strings.Contains(part.Content, "not executed") {
							refusals[part.ToolCallID]++
						}
					}
				}
				if len(calls) != 2 || len(refusals) != 2 {
					t.Fatal("refused tool calls were lost or left without results")
				}
				for id, count := range calls {
					if count != 1 || refusals[id] != 1 {
						t.Fatalf("unpaired refused call %s", id)
					}
				}
			}
		})
	}
}

func TestSubagentQueuedFollowupSharesOriginalTurnBudget(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	model := &recordingModel{complete: func(_ context.Context, _ string,
		_ []conversation.Message, _ []agent.ToolDefinition, call int,
	) (agent.ModelClientResult, error) {
		if call == 1 {
			close(started)
			<-release
		}
		return assistantResult("Answer."), nil
	}}
	manager := newTestManager(t, model)
	session, err := manager.Start(t.Context(), StartRequest{
		ModelRef: "child", Task: "Initial task.", AllowedTools: []string{"read_file"}, MaxTurns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := session.Write("Follow-up task."); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitDone(t, session)
	calls := model.snapshotCalls()
	if session.Status != "completed" || len(calls) != 2 ||
		!messagesContainText(calls[1], `remaining_turns="1"`) ||
		!messagesContainText(calls[1], "Follow-up task.") || len(model.snapshotTools()[1]) != 0 {
		t.Fatalf("queued follow-up reset or lost the turn budget: %s", session.Read(0, 2000))
	}
}
