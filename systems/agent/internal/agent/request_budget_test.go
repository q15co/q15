package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestRequestBudgetIncludesDefinitionsReplayAndEscaping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []conversation.Message
		tools    []ToolDefinition
	}{
		{name: "text", messages: []conversation.Message{conversation.UserMessage(strings.Repeat("x", 1024))}},
		{name: "escaping", messages: []conversation.Message{conversation.UserMessage(strings.Repeat("\x01", 256))}},
		{name: "tools", tools: []ToolDefinition{{Name: "test", Description: strings.Repeat("x", 1024)}}},
		{name: "replay", messages: []conversation.Message{{Role: conversation.AssistantRole, Parts: []conversation.Part{{Type: conversation.ReasoningPartType, Text: "synthetic", Replay: map[string]json.RawMessage{"synthetic": json.RawMessage(`"` + strings.Repeat("x", 1024) + `"`)}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModelClient{}
			var registry ToolRegistry
			if len(tc.tools) > 0 {
				var err error
				registry, err = NewToolRegistry(&testTool{def: tc.tools[0]})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := NewEngine(
				model,
				registry,
				[]string{"first"},
			).Run(context.Background(), EngineRequest{Messages: tc.messages, MaxRequestBytes: 512, UseTools: len(tc.tools) > 0})
			if len(model.callModels) != 0 {
				t.Fatal("oversized request reached provider")
			}
			var budget *RequestBudgetError
			if !errors.As(err, &budget) || budget.Bytes <= 512 {
				t.Fatalf("budget error=%v", err)
			}
			if budget.Bytes != budget.PromptBytes+budget.InputBytes+budget.PriorTurnBytes+budget.EvidenceBytes+budget.ToolDefinitionBytes+budget.FramingBytes {
				t.Fatalf("component sizes do not sum to canonical size: %+v", budget)
			}
		})
	}
}

func TestRequestBudgetLimitsAccumulationWithoutDiscardingEarlierEvidence(t *testing.T) {
	registry, err := NewToolRegistry(
		&testTool{
			def: ToolDefinition{Name: "read"},
			run: func(context.Context, string) (string, error) { return strings.Repeat("e", 16*1024), nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	observedError := false
	model := &fakeModelClient{
		complete: func(_ context.Context, _ string, messages []conversation.Message, tools []ToolDefinition) (ModelClientResult, error) {
			calls++
			size, err := measureRequest(messages, tools)
			if err != nil {
				t.Fatal(err)
			}
			if size.total > 256*1024 {
				t.Fatal("aggregate request exceeded budget")
			}
			if calls > 1 &&
				!strings.Contains(
					conversation.TextValue(messages[0]),
					"authoritative synthetic chunk",
				) {
				t.Fatal("source evidence was removed")
			}
			for _, m := range messages {
				for _, p := range m.Parts {
					observedError = observedError || p.IsError
				}
			}
			if calls == 30 {
				return assistantResult("finished"), nil
			}
			return toolCallResult(
				fmt.Sprintf("read-%d", calls),
				"read",
				fmt.Sprintf(`{"page":%d}`, calls),
			), nil
		},
	}
	engine := NewEngine(model, registry, []string{"first"})
	engine.SetMaxTurns(32)
	result, err := engine.Run(
		context.Background(),
		EngineRequest{
			Messages: []conversation.Message{
				conversation.SystemMessage("authoritative synthetic chunk"),
			},
			UseTools:        true,
			MaxRequestBytes: 256 * 1024,
		},
	)
	if err != nil || result.FinalText != "finished" || !observedError {
		t.Fatalf("bounded accumulation: calls=%d omitted=%v error=%v", calls, observedError, err)
	}
}

func TestRequestBudgetBoundsAccumulatedPriorTurns(t *testing.T) {
	registry, err := NewToolRegistry(
		&testTool{
			def: ToolDefinition{Name: "read"},
			run: func(context.Context, string) (string, error) { return strings.Repeat("e", 12*1024), nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	omitted := false
	model := &fakeModelClient{
		complete: func(_ context.Context, _ string, messages []conversation.Message, tools []ToolDefinition) (ModelClientResult, error) {
			calls++
			size, err := measureRequest(messages, tools)
			if err != nil {
				t.Fatal(err)
			}
			if size.total > 256*1024 {
				t.Fatalf("provider received oversized request: %d", size.total)
			}
			if conversation.TextValue(messages[0]) != "authoritative synthetic chunk" {
				t.Fatal("source evidence was removed")
			}
			for _, m := range messages {
				for _, p := range m.Parts {
					omitted = omitted || strings.Contains(p.Content, "omitted") ||
						strings.Contains(p.Text, "omitted") ||
						strings.Contains(p.Arguments, "omitted")
				}
			}
			if calls == 24 {
				return assistantResult("finished"), nil
			}
			result := toolCallResult(
				fmt.Sprintf("read-%d", calls),
				"read",
				fmt.Sprintf(`{"page":%d,"pad":"%s"}`, calls, strings.Repeat("a", 8*1024)),
			)
			result.Messages[0].Parts = append(
				result.Messages[0].Parts,
				conversation.Text(strings.Repeat("t", 8*1024), ""),
				conversation.Reasoning(
					strings.Repeat("r", 8*1024),
					map[string]json.RawMessage{
						"synthetic": json.RawMessage(`"` + strings.Repeat("p", 8*1024) + `"`),
					},
				),
			)
			return result, nil
		},
	}
	engine := NewEngine(model, registry, []string{"first"})
	engine.SetMaxTurns(32)
	result, err := engine.Run(
		context.Background(),
		EngineRequest{
			Messages: []conversation.Message{
				conversation.SystemMessage("authoritative synthetic chunk"),
			},
			UseTools:        true,
			MaxRequestBytes: 256 * 1024,
		},
	)
	if err != nil || result.FinalText != "finished" || calls != 24 || !omitted {
		t.Fatalf(
			"prior turns did not converge: calls=%d omitted=%v error=%v",
			calls,
			omitted,
			err,
		)
	}
	recorded := 0
	for _, m := range result.Messages {
		for _, p := range m.Parts {
			if len(p.Arguments) >= 8*1024 || len(p.Text) >= 8*1024 || len(p.Content) >= 8*1024 {
				recorded++
			}
		}
	}
	if recorded == 0 {
		t.Fatal("request reduction rewrote the recorded run")
	}
}

func TestRequestBudgetDisabledPreservesLargeInteractiveInput(t *testing.T) {
	model := &fakeModelClient{results: []ModelClientResult{assistantResult("finished")}}
	result, err := NewEngine(
		model,
		nil,
		[]string{"first"},
	).Run(context.Background(), EngineRequest{Messages: []conversation.Message{conversation.UserMessage(strings.Repeat("x", 2*1024*1024))}})
	if err != nil || result.FinalText != "finished" || len(model.callModels) != 1 {
		t.Fatalf("unbounded caller changed: %v", err)
	}
}

func TestRequestBudgetReducesAccumulatedToolCallsBeforeNextProviderTurn(t *testing.T) {
	initial := conversation.SystemMessage("Synthetic conversion source.")
	first := toolCallResult("call-1", "read", `{"query":"`+strings.Repeat("a", 24*1024)+`"}`)
	second := toolCallResult("call-2", "read", `{"query":"`+strings.Repeat("b", 24*1024)+`"}`)
	omitted := toolResultMessage(
		"call-1",
		ToolResult{Output: "tool error: " + omittedToolEvidence},
		true,
	)
	beforeLastEvidence := append([]conversation.Message{initial}, first.Messages...)
	beforeLastEvidence = append(beforeLastEvidence, omitted)
	beforeLastEvidence = append(beforeLastEvidence, second.Messages...)
	toolRuns := 0
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "read"},
		run: func(context.Context, string) (string, error) {
			toolRuns++
			return strings.Repeat("e", 64*1024), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	size, err := measureRequest(beforeLastEvidence, registry.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	limit := size.total + 16
	model := &fakeModelClient{
		results: []ModelClientResult{first, second, assistantResult("finished")},
	}
	result, err := NewEngine(
		model,
		registry,
		[]string{"first", "fallback"},
	).Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{initial}, UseTools: true, MaxRequestBytes: limit,
	})
	if err != nil || result.FinalText != "finished" || result.Turn != 2 {
		t.Fatalf("accumulated tool calls were not reduced: turn=%d error=%v", result.Turn, err)
	}
	if len(model.callModels) != 3 || toolRuns != 2 {
		t.Fatalf(
			"reduced accumulation did not reach the provider again: calls=%v tool_runs=%d",
			model.callModels,
			toolRuns,
		)
	}
	last := model.callMsgs[2]
	if !strings.Contains(last[1].Parts[0].Arguments, "omitted") ||
		len(last[3].Parts[0].Arguments) < 24*1024 {
		t.Fatalf(
			"oldest-first argument reduction = %d bytes, %d bytes",
			len(last[1].Parts[0].Arguments),
			len(last[3].Parts[0].Arguments),
		)
	}
	if len(result.Messages) != 5 ||
		result.Messages[0].Parts[0].Arguments != first.Messages[0].Parts[0].Arguments ||
		result.Messages[2].Parts[0].Arguments != second.Messages[0].Parts[0].Arguments {
		t.Fatal("budget enforcement discarded recorded assistant tool-call arguments")
	}
	if result.Messages[4].Parts[0].Text != "finished" {
		t.Fatalf("reduced run did not record the finishing reply: %#v", result.Messages[4])
	}
}

func TestRequestBudgetReducesEvidenceAfterAssistantReplayGrows(t *testing.T) {
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "read"},
		run: func(context.Context, string) (string, error) { return strings.Repeat("e", 24*1024), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	omitted := false
	model := &fakeModelClient{
		complete: func(_ context.Context, _ string, messages []conversation.Message, tools []ToolDefinition) (ModelClientResult, error) {
			calls++
			size, err := measureRequest(messages, tools)
			if err != nil || size.total > 256*1024 {
				t.Fatalf("provider received oversized request: %d, %v", size.total, err)
			}
			if conversation.TextValue(messages[0]) != "authoritative source" {
				t.Fatal("source changed")
			}
			for _, message := range messages {
				for _, part := range message.Parts {
					omitted = omitted || strings.Contains(part.Content, "omitted")
				}
			}
			if calls == 10 {
				return assistantResult("finished"), nil
			}
			result := toolCallResult(fmt.Sprint(calls), "read", `{}`)
			result.Messages[0].Parts = append(
				result.Messages[0].Parts,
				conversation.Text(strings.Repeat("a", 25*1024), ""),
			)
			return result, nil
		},
	}
	result, err := NewEngine(
		model,
		registry,
		[]string{"first"},
	).Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{conversation.SystemMessage("authoritative source")},
		UseTools: true, MaxRequestBytes: 256 * 1024,
	})
	if err != nil || result.FinalText != "finished" || !omitted {
		t.Fatalf(
			"replay did not reduce evidence: calls=%d omitted=%v error=%v",
			calls,
			omitted,
			err,
		)
	}
	if result.Messages[1].Parts[0].Content != strings.Repeat("e", 24*1024) {
		t.Fatal("request reduction changed the recorded tool result")
	}
}

func TestRequestBudgetDefersBeforeTurnWhenReserveIsExhausted(t *testing.T) {
	const limit = 64 * 1024
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "read"},
		run: func(context.Context, string) (string, error) { return "ok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Size the irreducible prompt so the first request fits, but one round of
	// irreducible assistant/tool shells lands past the reply reserve even
	// after every reduction path runs.
	effective := limit - limit/8
	probe, err := measureRequest(
		[]conversation.Message{conversation.SystemMessage("p")},
		registry.Definitions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	target := effective - 100
	initial := conversation.SystemMessage(strings.Repeat("p", target-probe.total+1))
	actual, err := measureRequest([]conversation.Message{initial}, registry.Definitions())
	if err != nil || actual.total != target {
		t.Fatalf("fixture sizing: total=%d target=%d error=%v", actual.total, target, err)
	}
	model := &fakeModelClient{results: []ModelClientResult{
		toolCallResult("read-1", "read", `{"page":1}`),
	}}
	result, err := NewEngine(
		model,
		registry,
		[]string{"first"},
	).Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{initial}, UseTools: true, MaxRequestBytes: limit,
	})
	var budget *RequestBudgetError
	if !errors.As(err, &budget) || len(model.callModels) != 1 || result.Turn != 1 {
		t.Fatalf(
			"accumulation past the reserve was not deferred before the next turn: calls=%v turn=%d error=%v",
			model.callModels,
			result.Turn,
			err,
		)
	}
	if budget.Bytes != budget.PromptBytes+budget.InputBytes+budget.PriorTurnBytes+budget.EvidenceBytes+budget.ToolDefinitionBytes+budget.FramingBytes {
		t.Fatalf("component sizes do not sum to canonical size: %+v", budget)
	}
	if budget.PromptBytes <= limit/2 || budget.PromptBytes <= budget.PriorTurnBytes {
		t.Fatalf("oversized prompt component not named: %+v", budget)
	}
	if len(result.Messages) == 0 ||
		result.Messages[0].Parts[0].Arguments != `{"page":1}` {
		t.Fatal("budget enforcement rewrote the recorded assistant tool call")
	}
}
