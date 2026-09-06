package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/dump"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

func TestProviderStreamsThroughRuntimeRoutingAndPayloadCapture(t *testing.T) {
	for _, providerType := range []string{providertypes.Ollama, providertypes.OpenAICompatible} {
		t.Run(providerType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			firstDelta := make(chan struct{})
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if providerType == "ollama" {
						w.Header().Set("Content-Type", "application/x-ndjson")
						_, _ = io.WriteString(
							w,
							`{"message":{"content":"Hello"},"done":false}`+"\n",
						)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`+"\n\n")
					}
					w.(http.Flusher).Flush()
					// The runtime must expose the first fragment before the provider can
					// finish; any buffering wrapper would deadlock until the deadline.
					select {
					case <-firstDelta:
					case <-r.Context().Done():
						return
					}
					if providerType == "ollama" {
						_, _ = io.WriteString(
							w,
							`{"message":{"content":" world"},"done":true,"done_reason":"stop"}`+"\n",
						)
					} else {
						_, _ = io.WriteString(w, "data: "+`{"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
					}
				}),
			)
			t.Cleanup(server.Close)
			registry := testRegistry(t, map[string][]modelcatalog.Model{
				"provider": {
					{
						ProviderModel: "model",
						ProviderType:  providerType,
						Capabilities:  modelcatalog.Capabilities{Text: true},
					},
				},
			})
			var wire, canonical bytes.Buffer
			factory := makeDumpAwareFactory(dump.NewTransportDump(server.Client().Transport, &wire))
			adapter, err := newModelAdapterWithFactory(registry, nil, func(
				model modelcatalog.Model, media q15media.Store,
			) (agent.ModelClient, error) {
				model.ProviderBaseURL = server.URL
				model.ProviderAPIKey = "test-key"
				return factory(model, media)
			})
			if err != nil {
				t.Fatal(err)
			}
			client := dump.NewModelClientDump(adapter, &canonical)
			var deltas string
			result, err := agent.NewEngine(client, nil, []string{"model"}).
				Run(ctx, agent.EngineRequest{
					Messages: []conversation.Message{conversation.UserMessage("hello")},
					Observer: agent.RunObserverFunc(func(_ context.Context, event agent.RunEvent) {
						if event.Type != agent.RunEventModelTurnDelta {
							return
						}
						if deltas == "" {
							close(firstDelta)
						}
						deltas += event.Delta
					}),
				})
			if err != nil || deltas != "Hello world" || result.FinalText != deltas {
				t.Fatalf("runtime stream: deltas=%q final=%q err=%v", deltas, result.FinalText, err)
			}
			if !bytes.Contains(wire.Bytes(), []byte(`"type":"wire_response"`)) ||
				!bytes.Contains(canonical.Bytes(), []byte(`"type":"canonical_response"`)) {
				t.Fatalf(
					"missing completed payload capture: wire=%s canonical=%s",
					&wire,
					&canonical,
				)
			}
		})
	}
}
