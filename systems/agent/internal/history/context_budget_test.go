package history

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestContextUsesModelWindowThroughoutToolLoop(t *testing.T) {
	input := conversation.UserMessage("Inspect the evidence and finish the task.")
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	snapshot := contextSnapshot{
		systemCount: 1, input: []conversation.Message{input},
		settings: ContextSettings{InitialContextMaxTokens: 100000},
		groups: []contextGroup{
			{
				id:     1,
				kind:   "recent",
				digest: "Earlier work was completed.",
				messages: []conversation.Message{
					conversation.UserMessage("OLD_SOURCE " + strings.Repeat("older ", 80000)),
				},
			},
			{id: 2, kind: "recent", protected: true, messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.Text(strings.Repeat("prior ", 20000), ""),
				),
			}},
		},
	}
	output := strings.Repeat(" evidence", 70000)
	reasoning := strings.Repeat("Planning the evidence checks. ", 200)
	tools, err := agent.NewToolRegistry(&contextBudgetTool{output: output})
	if err != nil {
		t.Fatal(err)
	}
	var first []conversation.Message
	calls := 0
	client := &contextBudgetModel{
		model: agent.ModelContext{Ref: "p/large", MaxContextTokens: 1048576, MaxOutputTokens: 8192},
		complete: func(messages []conversation.Message) agent.ModelClientResult {
			calls++
			metadata := readWindowMetadata(t, messages)
			if metadata.Budget != 987956 ||
				metadata.Remaining != metadata.Budget-metadata.Estimated {
				t.Fatalf("incorrect usable context: %+v", metadata)
			}
			if calls == 1 {
				if metadata.Estimated <= 16000 || metadata.Estimated > 100000 {
					t.Fatalf(
						"starting context did not use the actual model's assembly budget: %+v",
						metadata,
					)
				}
				if strings.Contains(conversation.TextValue(messages[1]), "OLD_SOURCE") {
					t.Fatal("starting history exceeded its assembly budget")
				}
				first = conversation.CloneMessages(messages[:len(messages)-1])
				return agent.ModelClientResult{Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.Reasoning(reasoning, nil),
						conversation.ToolCall("read", "read_file", `{}`),
					),
				}}
			}
			if !reflect.DeepEqual(messages[:len(first)], first) {
				t.Fatal("accumulated work changed the starting context")
			}
			if metadata.Estimated <= 100000 ||
				messages[len(messages)-2].Parts[0].Content != output {
				t.Fatal("active evidence was shortened even though it fits the model window")
			}
			if messages[len(messages)-3].Parts[0].Text != reasoning {
				t.Fatal("active reasoning text was shortened below the model window")
			}
			return agent.ModelClientResult{Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("Evidence checked.", "")),
			}}
		},
	}
	engine := agent.NewEngine(client, tools, []string{client.model.Ref})
	result, err := engine.Run(t.Context(), agent.EngineRequest{
		Messages: base, UseTools: true, ContextFitter: snapshot.fitRequest,
	})
	if err != nil || calls != 2 || result.FinalText != "Evidence checked." {
		t.Fatalf("tool loop failed: calls=%d result=%+v error=%v", calls, result, err)
	}
	if result.Messages[1].Parts[0].Content != output {
		t.Fatal("provider fitting modified the canonical tool result")
	}
	for _, message := range result.Messages {
		if strings.Contains(conversation.TextValue(message), "<context_window ") {
			t.Fatal("request counters leaked into the transcript")
		}
	}
}

func TestContextModelWindowAllowsLargeEncodedEvidence(t *testing.T) {
	input := conversation.UserMessage("Inspect this result.")
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	output := strings.Repeat(" configuration", 400000)
	request := append(conversation.CloneMessages(base),
		conversation.AssistantMessage(conversation.ToolCall("read", "read_file", `{}`)),
		conversation.Message{Role: conversation.ToolRole, Parts: []conversation.Part{
			conversation.ToolResult("read", output, false),
		}},
	)
	settings, err := normalizeContextSettings(ContextSettings{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contextSnapshot{
		systemCount: 1,
		input:       []conversation.Message{input},
		settings:    settings,
	}
	result, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/large", MaxContextTokens: 1048576,
	}, request, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	size, err := agent.RequestEncodedBytes(result, nil)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 4*1024*1024 || result[len(result)-2].Parts[0].Content != output {
		t.Fatal("evidence was shortened below the model window")
	}
	if request[len(request)-1].Parts[0].Content != output {
		t.Fatal("fitting modified the original evidence")
	}
}

func TestContextMandatoryInputCanExceedAssemblyTarget(t *testing.T) {
	input := conversation.UserMessage(strings.Repeat(" question", 90000))
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	snapshot := contextSnapshot{systemCount: 1, input: []conversation.Message{input},
		settings: ContextSettings{InitialContextMaxTokens: 100000},
		groups:   []contextGroup{{id: 1, kind: "gap", digest: "Optional older work."}},
	}
	result, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/large", MaxContextTokens: 1048576,
	}, base, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result[len(result)-2], input) ||
		readWindowMetadata(t, result).Estimated <= 100000 {
		t.Fatal("mandatory input was constrained by the history assembly target")
	}
	_, err = snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/small", MaxContextTokens: 8192,
	}, base, nil, len(base))
	var budget *agent.TokenBudgetError
	if !errors.As(err, &budget) || budget.LimitTokens != 5735 {
		t.Fatalf("small model did not enforce its actual window: %v", err)
	}
}

func TestContextModelGrowthKeepsStartingHistorySelection(t *testing.T) {
	input := conversation.UserMessage("Continue.")
	base := []conversation.Message{conversation.SystemMessage("Instructions."), input}
	snapshot := contextSnapshot{systemCount: 1, input: []conversation.Message{input},
		settings: ContextSettings{InitialContextMaxFraction: 0.125},
		groups: []contextGroup{
			{id: 1, kind: "recent", digest: "Older work summary.", messages: []conversation.Message{
				conversation.UserMessage("OLD_SOURCE " + strings.Repeat("earlier ", 5000)),
			}},
			{id: 2, kind: "recent", protected: true, messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("Latest answer.", "")),
			}},
		},
	}
	first, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/small", MaxContextTokens: 8192,
	}, base, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/large", MaxContextTokens: 1048576,
	}, base, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first[:len(first)-1], second[:len(second)-1]) {
		t.Fatal("larger model reintroduced history omitted during initial assembly")
	}
	if readWindowMetadata(t, first).Budget != 5735 ||
		readWindowMetadata(t, second).Budget != 987956 {
		t.Fatal("model change or initial caps constrained the runtime window")
	}
	output := strings.Repeat(" evidence", 10000)
	request := append(conversation.CloneMessages(base),
		conversation.AssistantMessage(conversation.ToolCall("read", "read_file", `{}`)),
		conversation.Message{Role: conversation.ToolRole, Parts: []conversation.Part{
			conversation.ToolResult("read", output, false),
		}},
	)
	small, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/small", MaxContextTokens: 8192,
	}, request, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	if readWindowMetadata(t, small).Estimated > 5735 ||
		small[len(small)-2].Parts[0].Content == output {
		t.Fatal("actual small-model capacity did not constrain accumulated evidence")
	}
	large, err := snapshot.fitRequest(t.Context(), agent.ModelContext{
		Ref: "p/large", MaxContextTokens: 1048576,
	}, request, nil, len(base))
	if err != nil {
		t.Fatal(err)
	}
	if large[len(large)-2].Parts[0].Content != output ||
		!reflect.DeepEqual(first[:len(first)-1], large[:len(first)-1]) {
		t.Fatal("fitting to a smaller model permanently changed history or evidence")
	}
}

type contextBudgetModel struct {
	model    agent.ModelContext
	complete func([]conversation.Message) agent.ModelClientResult
}

func (m *contextBudgetModel) ContextForModel(string) agent.ModelContext { return m.model }

func (m *contextBudgetModel) Complete(_ context.Context, _ string,
	messages []conversation.Message, _ []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	return m.complete(messages), nil
}

type contextBudgetTool struct{ output string }

func (*contextBudgetTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "read_file"}
}

func (t *contextBudgetTool) Run(context.Context, string) (string, error) { return t.output, nil }
