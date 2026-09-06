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
}
