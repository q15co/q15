package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type streamingAdapterTestClient struct {
	fakeModelClient
	streamCalls int
}

func (c *streamingAdapterTestClient) CompleteStream(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	onDelta func(string),
) (agent.ModelClientResult, error) {
	c.streamCalls++
	onDelta("ok")
	return c.Complete(ctx, model, messages, tools)
}

type reasoningAdapterTestClient struct {
	streamingAdapterTestClient
	reasoningCalls int
}

func (c *reasoningAdapterTestClient) CompleteStreamWithReasoning(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	onDelta func(string),
	onReasoning func(string),
) (agent.ModelClientResult, error) {
	c.reasoningCalls++
	if onReasoning != nil {
		onReasoning("consider carefully")
	}
	if onDelta != nil {
		onDelta("ok")
	}
	return c.Complete(ctx, model, messages, tools)
}

func TestModelAdapterForwardsReasoningThroughRoutedAndBoundClients(t *testing.T) {
	for _, bound := range []bool{false, true} {
		name := "routed"
		if bound {
			name = "bound"
		}
		t.Run(name, func(t *testing.T) {
			registry := testRegistry(t, map[string][]modelcatalog.Model{
				"provider": {
					{
						ProviderModel: "model:cloud",
						Capabilities:  modelcatalog.Capabilities{Text: true},
					},
				},
			})
			want := agent.ModelClientResult{
				Messages: []conversation.Message{conversation.AssistantMessage(
					conversation.Reasoning("consider carefully", nil), conversation.Text("ok", ""),
				)},
			}
			inner := &reasoningAdapterTestClient{
				streamingAdapterTestClient: streamingAdapterTestClient{
					fakeModelClient: fakeModelClient{
						results: []agent.ModelClientResult{want, want, want},
					},
				},
			}
			adapter, err := newModelAdapterWithFactory(
				registry,
				nil,
				func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return inner, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			var client agent.ReasoningStreamingModelClient = adapter
			if bound {
				pinned, err := adapter.BindProviderModel("provider", "model")
				if err != nil {
					t.Fatal(err)
				}
				client = pinned.(agent.ReasoningStreamingModelClient)
			}
			messages := []conversation.Message{conversation.UserMessageParts(
				conversation.Text(
					"describe",
					"",
				),
				conversation.Image("media://sha256/missing", "image/png"),
			)}
			tools := []agent.ToolDefinition{{Name: "shell"}}
			var fragments []string
			got, err := client.CompleteStreamWithReasoning(
				context.Background(),
				"model",
				messages,
				tools,
				func(text string) { fragments = append(fragments, "content: "+text) },
				func(text string) { fragments = append(fragments, "reasoning: "+text) },
			)
			if err != nil || !reflect.DeepEqual(got, want) ||
				!reflect.DeepEqual(
					fragments,
					[]string{"reasoning: consider carefully", "content: ok"},
				) {
				t.Fatalf("reasoning stream result=%#v fragments=%v err=%v", got, fragments, err)
			}
			// A reasoning-only subscriber must also select the extended provider API.
			var reasoning string
			got, err = client.CompleteStreamWithReasoning(
				context.Background(),
				"model",
				messages,
				tools,
				nil,
				func(text string) { reasoning += text },
			)
			if err != nil || reasoning != "consider carefully" || !reflect.DeepEqual(got, want) {
				t.Fatalf(
					"reasoning-only stream result=%#v reasoning=%q err=%v",
					got,
					reasoning,
					err,
				)
			}
			batch, err := client.Complete(context.Background(), "model", messages, tools)
			if err != nil || !reflect.DeepEqual(batch, want) || inner.reasoningCalls != 2 ||
				inner.streamCalls != 0 {
				t.Fatalf("batch parity result=%#v reasoning calls=%d stream calls=%d err=%v",
					batch, inner.reasoningCalls, inner.streamCalls, err)
			}
			if len(inner.calls) != 3 || !reflect.DeepEqual(inner.calls[0], inner.calls[2]) ||
				inner.calls[0].model != "model:cloud" || len(inner.calls[0].tools) != 0 {
				t.Fatalf("reasoning request adaptation differs: %#v", inner.calls)
			}
			for _, part := range inner.calls[0].messages[0].Parts {
				if part.Type == conversation.MediaPartType {
					t.Fatal("unsupported image reached reasoning provider")
				}
			}
			if _, err := client.CompleteStreamWithReasoning(context.Background(), "missing", nil, nil,
				nil, func(string) {}); err == nil {
				t.Fatal("reasoning stream accepted unknown or mismatched model")
			}
		})
	}
}

func TestModelAdapterStreamsThroughRoutingAndCapabilityAdaptation(t *testing.T) {
	for _, bound := range []bool{false, true} {
		name := "routed"
		if bound {
			name = "bound"
		}
		t.Run(name, func(t *testing.T) {
			registry := testRegistry(t, map[string][]modelcatalog.Model{
				"provider": {
					{
						ProviderModel: "model:cloud",
						Capabilities:  modelcatalog.Capabilities{Text: true},
					},
				},
			})
			inner := &streamingAdapterTestClient{}
			adapter, err := newModelAdapterWithFactory(
				registry,
				nil,
				func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return inner, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			var client agent.StreamingModelClient = adapter
			if bound {
				pinned, err := adapter.BindProviderModel("provider", "model")
				if err != nil {
					t.Fatal(err)
				}
				client = pinned.(agent.StreamingModelClient)
			}
			messages := []conversation.Message{conversation.UserMessageParts(
				conversation.Text(
					"describe",
					"",
				),
				conversation.Image("media://sha256/missing", "image/png"),
			)}
			tools := []agent.ToolDefinition{{Name: "shell"}}
			var delta string
			got, err := client.CompleteStream(context.Background(), "model", messages, tools,
				func(text string) { delta += text })
			if err != nil {
				t.Fatal(err)
			}
			want, err := client.Complete(context.Background(), "model", messages, tools)
			if err != nil {
				t.Fatal(err)
			}
			if delta != "ok" || inner.streamCalls != 1 || !reflect.DeepEqual(got, want) {
				t.Fatalf(
					"stream delta=%q calls=%d result=%#v want=%#v",
					delta,
					inner.streamCalls,
					got,
					want,
				)
			}
			if len(inner.calls) != 2 || !reflect.DeepEqual(inner.calls[0], inner.calls[1]) {
				t.Fatalf("batch and stream request adaptation differ: %#v", inner.calls)
			}
			call := inner.calls[0]
			if call.model != "model:cloud" || len(call.tools) != 0 {
				t.Fatalf("provider request model=%q tools=%v", call.model, call.tools)
			}
			for _, part := range call.messages[0].Parts {
				if part.Type == conversation.MediaPartType {
					t.Fatal("unsupported image reached text-only provider")
				}
			}
			if _, err := client.CompleteStream(context.Background(), "missing", nil, nil, func(string) {}); err == nil {
				t.Fatal("stream accepted unknown or mismatched model")
			}
			delta = ""
			got, err = client.(agent.ReasoningStreamingModelClient).CompleteStreamWithReasoning(
				context.Background(), "model", messages, tools,
				func(text string) { delta += text },
				func(string) { t.Fatal("content-only provider synthesized reasoning") },
			)
			if err != nil || delta != "ok" || inner.streamCalls != 2 ||
				!reflect.DeepEqual(got, want) {
				t.Fatalf(
					"reasoning fallback delta=%q calls=%d result=%#v err=%v",
					delta,
					inner.streamCalls,
					got,
					err,
				)
			}
		})
	}
}

func TestModelAdapterStreamingFallsBackToBatchProvider(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"provider": {
			{
				ProviderModel: "model",
				Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
			},
		},
	})
	inner := &fakeModelClient{}
	adapter, err := newModelAdapterWithFactory(registry, nil,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return inner, nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := adapter.CompleteStream(
		context.Background(),
		"model",
		nil,
		[]agent.ToolDefinition{{Name: "shell"}},
		func(string) { t.Fatal("batch provider synthesized a delta") },
	)
	if err != nil || len(got.Messages) != 1 || len(inner.calls) != 1 ||
		len(inner.calls[0].tools) != 1 {
		t.Fatalf("batch fallback result=%#v calls=%#v err=%v", got, inner.calls, err)
	}
	got, err = adapter.CompleteStreamWithReasoning(context.Background(), "model", nil, nil,
		func(string) { t.Fatal("batch provider synthesized content") },
		func(string) { t.Fatal("batch provider synthesized reasoning") })
	if err != nil || len(got.Messages) != 1 || len(inner.calls) != 2 {
		t.Fatalf("reasoning batch fallback result=%#v calls=%#v err=%v", got, inner.calls, err)
	}
}
