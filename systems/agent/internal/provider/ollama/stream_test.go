package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestStreamCollectorForwardsOnlyContentAndPreservesResponse(t *testing.T) {
	var deltas []string
	collector := newStreamCollector(func(delta string) {
		deltas = append(deltas, delta)
	})
	var args ollamaapi.ToolCallFunctionArguments
	if err := json.Unmarshal([]byte(`{"path":"README.md"}`), &args); err != nil {
		t.Fatal(err)
	}
	responses := []ollamaapi.ChatResponse{
		{Message: ollamaapi.Message{Thinking: "considering "}},
		{Message: ollamaapi.Message{Thinking: "carefully", Content: "  checking"}},
		{Message: ollamaapi.Message{}},
		{Message: ollamaapi.Message{Content: " "}},
		{Message: ollamaapi.Message{ToolCalls: []ollamaapi.ToolCall{{
			Function: ollamaapi.ToolCallFunction{Name: "read_file", Arguments: args},
		}}}},
		{
			Message:    ollamaapi.Message{Content: "now  "},
			Done:       true,
			DoneReason: "stop",
			Metrics:    ollamaapi.Metrics{PromptEvalCount: 12, EvalCount: 7},
		},
	}
	wantCounts := []int{0, 1, 1, 2, 2, 3}
	for i, response := range responses {
		if err := collector.Record(response); err != nil {
			t.Fatalf("Record(%d): %v", i, err)
		}
		if len(deltas) != wantCounts[i] {
			t.Fatalf("Record(%d) forwarded %d deltas, want %d", i, len(deltas), wantCounts[i])
		}
	}
	if want := []string{"  checking", " ", "now  "}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("deltas = %#v, want %#v", deltas, want)
	}
	result, err := collector.Result()
	if err != nil {
		t.Fatal(err)
	}
	want := agent.ModelClientResult{
		Messages: []conversation.Message{conversation.AssistantMessage(
			conversation.Reasoning("considering carefully", map[string]json.RawMessage{
				ollamaReplayKey: json.RawMessage(`{"thinking":"considering carefully"}`),
			}),
			conversation.Text("checking now", ""),
			conversation.ToolCall("ollama-call-1", "read_file", `{"path":"README.md"}`),
		)},
		FinishReason: "stop",
		Usage:        agent.ModelUsage{InputTokens: 12, OutputTokens: 7, TotalTokens: 19},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("Result() = %#v, want %#v", result, want)
	}
}

func TestCompleteStreamMatchesCompleteForNativeAndBearerClients(t *testing.T) {
	for _, apiKey := range []string{"", "ollama-key"} {
		t.Run("api_key="+apiKey, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req ollamaapi.ChatRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if req.Stream == nil || !*req.Stream || req.Model != "model" ||
						len(req.Tools) != 1 {
						t.Errorf("unexpected request: %#v", req)
					}
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, strings.Join([]string{
						`{"message":{"thinking":"considering ","content":"hel"},"done":false}`,
						`{"message":{"thinking":"carefully"},"done":false}`,
						`{"message":{"content":"lo"},"done":false}`,
						`{"message":{"tool_calls":[{"id":"call-1","function":{"name":"read_file","arguments":{"path":"README.md"}}}]},"done":false}`,
						`{"message":{},"done":true,"done_reason":"stop","prompt_eval_count":12,"eval_count":7}`,
					}, "\n")+"\n")
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL, apiKey, nil, server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			messages := []conversation.Message{conversation.UserMessage("hello")}
			tools := []agent.ToolDefinition{{Name: "read_file"}}
			batch, err := client.Complete(context.Background(), "model", messages, tools)
			if err != nil {
				t.Fatal(err)
			}
			var deltas []string
			streamed, err := client.CompleteStream(
				context.Background(),
				"model",
				messages,
				tools,
				func(delta string) {
					deltas = append(deltas, delta)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(streamed, batch) {
				t.Fatalf("streamed = %#v, batch = %#v", streamed, batch)
			}
			if want := []string{"hel", "lo"}; !reflect.DeepEqual(deltas, want) {
				t.Fatalf("deltas = %#v, want %#v", deltas, want)
			}
			if len(streamed.Messages) != 1 || len(streamed.Messages[0].Parts) != 3 {
				t.Fatalf("expected reasoning, content, and tool call; got %#v", streamed.Messages)
			}
			if streamed.Usage.TotalTokens != 19 || streamed.FinishReason != "stop" {
				t.Fatalf("lost usage or finish reason: %#v", streamed)
			}
		})
	}
}

func TestCompleteStreamEmitsBeforeResponseFinishes(t *testing.T) {
	for _, apiKey := range []string{"", "ollama-key"} {
		t.Run("api_key="+apiKey, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			firstDelta := make(chan struct{})
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, `{"message":{"content":"hello"},"done":false}`+"\n")
					w.(http.Flusher).Flush()
					select {
					case <-firstDelta:
					case <-ctx.Done():
						return
					}
					_, _ = io.WriteString(w, `{"message":{},"done":true,"done_reason":"stop"}`+"\n")
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL, apiKey, nil, server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.CompleteStream(ctx, "model", nil, nil, func(delta string) {
				if delta != "hello" {
					t.Errorf("delta = %q, want hello", delta)
				}
				close(firstDelta)
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := conversation.FinalAnswer(result.Messages); got != "hello" {
				t.Fatalf("FinalAnswer() = %q, want hello", got)
			}
		})
	}
}

func TestCompleteStreamRejectsFailedAndTruncatedResponses(t *testing.T) {
	for _, apiKey := range []string{"", "ollama-key"} {
		for _, tc := range []struct {
			name string
			body string
			want string
		}{
			{name: "empty", want: "no responses"},
			{name: "truncated", body: `{"message":{"content":"partial"},"done":false}`, want: "unexpected EOF"},
			{name: "provider error", body: `{"error":"model unavailable"}`, want: "model unavailable"},
			{name: "malformed", body: `{"message":`},
			{
				name: "error after content",
				body: `{"message":{"content":"partial"},"done":false}` + "\n" + `{"error":"generation failed"}`,
				want: "generation failed",
			},
		} {
			t.Run("api_key="+apiKey+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/x-ndjson")
						_, _ = io.WriteString(w, tc.body)
					}),
				)
				defer server.Close()
				client, err := NewClient(server.URL, apiKey, nil, server.Client().Transport)
				if err != nil {
					t.Fatal(err)
				}
				result, err := client.CompleteStream(
					context.Background(),
					"model",
					nil,
					nil,
					func(string) {},
				)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
				if tc.name == "truncated" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
				}
				if !reflect.DeepEqual(result, agent.ModelClientResult{}) {
					t.Fatalf("failed stream returned partial result: %#v", result)
				}
			})
		}
	}
}

func TestCompleteStreamCancellationStopsCallbacks(t *testing.T) {
	for _, apiKey := range []string{"", "ollama-key"} {
		t.Run("api_key="+apiKey, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/x-ndjson")
					_, _ = io.WriteString(w, `{"message":{"content":"first"},"done":false}`+"\n"+
						`{"message":{"content":"second"},"done":true}`+"\n")
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL, apiKey, nil, server.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deltas []string
			result, err := client.CompleteStream(ctx, "model", nil, nil, func(delta string) {
				deltas = append(deltas, delta)
				cancel()
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if want := []string{"first"}; !reflect.DeepEqual(deltas, want) {
				t.Fatalf("deltas = %#v, want %#v", deltas, want)
			}
			if !reflect.DeepEqual(result, agent.ModelClientResult{}) {
				t.Fatalf("canceled stream returned partial result: %#v", result)
			}
		})
	}
}
