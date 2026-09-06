package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type streamingTestClient struct {
	fakeModelClient
	stream func(context.Context, string, []conversation.Message, []ToolDefinition, func(string)) (ModelClientResult, error)
}

func (c *streamingTestClient) CompleteStream(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []ToolDefinition,
	onDelta func(string),
) (ModelClientResult, error) {
	return c.stream(ctx, model, messages, tools, onDelta)
}

func TestLoopStreamingPreservesResultsAndOrdersEvents(t *testing.T) {
	toolResult := toolCallResult("call-1", "read", `{}`)
	toolResult.Messages[0].Parts = append(
		[]conversation.Part{conversation.Text("Checking", "")},
		toolResult.Messages[0].Parts...)
	results := []ModelClientResult{toolResult, assistantResult("All done")}
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "read"},
		run: func(context.Context, string) (string, error) { return "file contents", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	batchStore := &fakeConversationStore{}
	input := userTextMessage("hello")
	input.UserTemporal = &conversation.UserTemporalMetadata{
		TimeLocal: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	}
	batch := NewLoop(
		&fakeModelClient{results: append([]ModelClientResult(nil), results...)},
		registry,
		[]string{"model"},
		DefaultSystemPrompt,
		batchStore,
		3,
	)
	want, err := batch.Reply(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}

	var events []RunEvent
	turn := 0
	client := &streamingTestClient{stream: func(
		ctx context.Context, model string, _ []conversation.Message, tools []ToolDefinition, onDelta func(string),
	) (ModelClientResult, error) {
		if model != "model" || len(tools) != 1 {
			t.Fatalf("request model=%q tools=%v", model, tools)
		}
		before := len(events)
		onDelta("")
		if len(events) != before {
			t.Fatal("empty delta was emitted")
		}
		fragments := [][]string{{"Check", "ing"}, {"All", " ", "done"}}[turn]
		for _, fragment := range fragments {
			onDelta(fragment)
			last := events[len(events)-1]
			if last.Type != RunEventModelTurnDelta || last.Delta != fragment ||
				last.Turn != turn || last.ModelRef != model || last.At.IsZero() {
				t.Fatalf("delta was not observed synchronously: %#v", last)
			}
		}
		result := results[turn]
		turn++
		return result, ctx.Err()
	}}
	streamStore := &fakeConversationStore{}
	loop := NewLoop(client, registry, []string{"model"}, DefaultSystemPrompt, streamStore, 3)
	got, err := loop.Reply(context.Background(), input, RunObserverFunc(
		func(_ context.Context, event RunEvent) { events = append(events, event) },
	))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) ||
		!reflect.DeepEqual(streamStore.lastAppend, batchStore.lastAppend) {
		t.Fatalf("stream changed final reply or transcript: got %#v, want %#v", got, want)
	}
	wantTypes := []RunEventType{
		RunEventRunStarted, RunEventModelTurnStarted, RunEventModelTurnDelta, RunEventModelTurnDelta,
		RunEventToolStarted, RunEventToolFinished, RunEventModelTurnStarted,
		RunEventModelTurnDelta, RunEventModelTurnDelta, RunEventModelTurnDelta, RunEventRunFinished,
	}
	var gotTypes []RunEventType
	for _, event := range events {
		gotTypes = append(gotTypes, event.Type)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event order = %v, want %v", gotTypes, wantTypes)
	}
	if events[len(events)-1].FinalText != want.Text {
		t.Fatalf("final event text = %q, want %q", events[len(events)-1].FinalText, want.Text)
	}
}

func TestEngineStreamingFallbackDiscardsFailedResult(t *testing.T) {
	var events []RunEvent
	client := &streamingTestClient{stream: func(
		_ context.Context, model string, _ []conversation.Message, _ []ToolDefinition, onDelta func(string),
	) (ModelClientResult, error) {
		if model == "first" {
			onDelta("incomplete")
			return assistantResult("incomplete"), errors.New("stream interrupted")
		}
		onDelta("complete")
		return assistantResult("complete"), nil
	}}
	engine := NewEngine(client, nil, []string{"first", "second"})
	got, err := engine.Run(context.Background(), EngineRequest{Observer: RunObserverFunc(
		func(_ context.Context, event RunEvent) { events = append(events, event) },
	)})
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalText != "complete" || len(got.Messages) != 1 ||
		messageText(got.Messages[0]) != "complete" {
		t.Fatalf("failed attempt leaked into result: %#v", got)
	}
	if len(events) != 4 || events[2].Type != RunEventModelTurnStarted ||
		events[2].ModelRef != "second" ||
		events[3].Delta != "complete" ||
		events[2].Turn != events[0].Turn {
		t.Fatalf("fallback attempt events = %#v", events)
	}
}

func TestLoopStreamingCancellationStopsFallbackAndPersistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &streamingTestClient{stream: func(
		ctx context.Context, _ string, _ []conversation.Message, _ []ToolDefinition, onDelta func(string),
	) (ModelClientResult, error) {
		calls++
		onDelta("partial")
		onDelta("discarded after cancellation")
		return assistantResult("partial"), ctx.Err()
	}}
	store := &fakeConversationStore{}
	loop := NewLoop(client, nil, []string{"first", "second"}, DefaultSystemPrompt, store, 3)
	var events []RunEvent
	_, err := loop.Reply(
		ctx,
		userTextMessage("hello"),
		RunObserverFunc(func(_ context.Context, event RunEvent) {
			events = append(events, event)
			if event.Type == RunEventModelTurnDelta {
				cancel()
			}
		}),
	)
	if !errors.Is(err, context.Canceled) || calls != 1 || store.appendCalls != 0 {
		t.Fatalf("canceled run: err=%v calls=%d persisted=%d", err, calls, store.appendCalls)
	}
	if len(events) != 4 || events[3].Type != RunEventRunFailed {
		t.Fatalf("cancellation events = %#v", events)
	}
}

func TestEngineWithoutObserverUsesBatchCompletion(t *testing.T) {
	client := &streamingTestClient{
		fakeModelClient: fakeModelClient{results: []ModelClientResult{assistantResult("batch")}},
		stream: func(context.Context, string, []conversation.Message, []ToolDefinition, func(string)) (ModelClientResult, error) {
			t.Fatal("unobserved run should use Complete")
			return ModelClientResult{}, nil
		},
	}
	got, err := NewEngine(client, nil, []string{"model"}).Run(context.Background(), EngineRequest{})
	if err != nil || got.FinalText != "batch" {
		t.Fatalf("unobserved run = %#v, %v", got, err)
	}
}

func TestEngineCancellationPreventsFurtherToolSideEffects(t *testing.T) {
	for _, cancelOnStart := range []bool{false, true} {
		name := "during tool"
		if cancelOnStart {
			name = "before tool"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			registry, err := NewToolRegistry(&testTool{
				def: ToolDefinition{Name: "mutate"},
				run: func(context.Context, string) (string, error) {
					// Deliberately ignores ctx like state-changing local tools. The
					// engine must prevent dispatching subsequent calls after Stop.
					calls++
					cancel()
					return "updated", nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			result := toolCallResult("first", "mutate", `{}`)
			result.Messages[0].Parts = append(
				result.Messages[0].Parts,
				conversation.ToolCall("second", "mutate", `{}`),
			)
			engine := NewEngine(
				&fakeModelClient{results: []ModelClientResult{result}},
				registry,
				[]string{"model"},
			)
			engine.SetMaxTurns(1)
			_, err = engine.Run(ctx, EngineRequest{UseTools: true, Observer: RunObserverFunc(
				func(_ context.Context, event RunEvent) {
					if cancelOnStart && event.Type == RunEventToolStarted {
						cancel()
					}
				},
			)})
			wantCalls := 1
			if cancelOnStart {
				wantCalls = 0
			}
			if !errors.Is(err, context.Canceled) || calls != wantCalls {
				t.Fatalf("canceled tool run: err=%v calls=%d want=%d", err, calls, wantCalls)
			}
			var stopErr *StopError
			if errors.As(err, &stopErr) {
				t.Fatalf("cancellation became a persistable turn-limit error: %v", stopErr)
			}
		})
	}
}
