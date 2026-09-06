package openaicompatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestCompleteStreamMatchesBatch(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		chunks       []string
		finishReason string
		deltas       []string
	}{
		{
			name:    "content and reasoning",
			message: `{"role":"assistant","content":"  Hello world!  ","reasoning_content":"private thoughts","reasoning_opaque":"opaque-token"}`,
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"private ","reasoning_opaque":"opaque-"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"thoughts","reasoning_opaque":"token"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"  Hello"}}]}`,
				`{"choices":[{"index":1,"delta":{"content":"ignored alternative"}},{"index":0,"delta":{"content":" world!  "}}]}`,
			},
			finishReason: "stop",
			deltas:       []string{"  Hello", " world!  "},
		},
		{
			name:    "parallel tool fragments",
			message: `{"role":"assistant","content":"Searching","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search","arguments":"{\"query\":\"weather\"}"}},{"id":"call-2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]}`,
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"content":"Searching","tool_calls":[{"index":1,"id":"call-2","type":"function","function":{"name":"read_","arguments":"{\"path\":"}},{"index":0,"id":"call-1","type":"function","function":{"name":"search","arguments":"{\"query\":\""}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"weather\"}"}},{"index":1,"function":{"name":"file","arguments":"\"a.txt\"}"}}]}}]}`,
			},
			finishReason: "tool_calls",
			deltas:       []string{"Searching"},
		},
		{
			name:         "refusal",
			message:      `{"role":"assistant","content":null,"refusal":"Cannot help."}`,
			chunks:       []string{`{"choices":[{"index":0,"delta":{"refusal":"Cannot help."}}]}`},
			finishReason: "content_filter",
		},
		{
			name:         "token limit",
			message:      `{"role":"assistant","content":"Partial"}`,
			chunks:       []string{`{"choices":[{"index":0,"delta":{"content":"Partial"}}]}`},
			finishReason: "length",
			deltas:       []string{"Partial"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batchRequests := make(chan map[string]any, 1)
			streamRequests := make(chan map[string]any, 1)
			client := newStreamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				if request["stream"] != true {
					batchRequests <- request
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(
						w,
						`{"choices":[{"index":0,"message":%s,"finish_reason":%q}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
						tt.message,
						tt.finishReason,
					)
					return
				}
				streamRequests <- request
				if r.Header.Get("Accept") != "text/event-stream" {
					t.Errorf("Accept = %q", r.Header.Get("Accept"))
				}
				if opts, ok := request["stream_options"].(map[string]any); !ok ||
					opts["include_usage"] != true {
					t.Errorf("stream_options = %#v, want include_usage", request["stream_options"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": keepalive\n\n")
				for _, chunk := range tt.chunks {
					writeStreamChunk(w, chunk)
				}
				writeStreamChunk(
					w,
					fmt.Sprintf(
						`{"choices":[{"index":0,"delta":{},"finish_reason":%q}]}`,
						tt.finishReason,
					),
				)
				writeStreamChunk(
					w,
					`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
				)
				writeStreamChunk(w, "[DONE]")
			})
			messages := []conversation.Message{conversation.UserMessage("Hello")}
			tools := []agent.ToolDefinition{{Name: "search", Description: "Search the web"}}
			batch, err := client.Complete(context.Background(), "model", messages, tools)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			var deltas []string
			stream, err := client.CompleteStream(
				context.Background(),
				"model",
				messages,
				tools,
				func(delta string) {
					deltas = append(deltas, delta)
				},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			if !reflect.DeepEqual(stream, batch) {
				t.Fatalf("stream = %#v, batch = %#v", stream, batch)
			}
			if stream.Usage != (agent.ModelUsage{InputTokens: 12, OutputTokens: 5, TotalTokens: 17}) {
				t.Errorf("usage = %#v", stream.Usage)
			}
			if !reflect.DeepEqual(deltas, tt.deltas) {
				t.Errorf("deltas = %#v, want %#v", deltas, tt.deltas)
			}
			batchRequest, streamRequest := <-batchRequests, <-streamRequests
			delete(streamRequest, "stream")
			delete(streamRequest, "stream_options")
			if !reflect.DeepEqual(streamRequest, batchRequest) {
				t.Errorf("stream request = %#v, batch request = %#v", streamRequest, batchRequest)
			}
		})
	}
}

func TestCompleteStreamDeliversBeforeCompletionAndStopsAtDone(t *testing.T) {
	firstDelta := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := newStreamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeStreamChunk(w, `{"choices":[{"index":0,"delta":{"content":"First"}}]}`)
		select {
		case <-firstDelta:
		case <-r.Context().Done():
			return
		}
		writeStreamChunk(
			w,
			`{"choices":[{"index":0,"delta":{"content":" second"},"finish_reason":"stop"}]}`,
		)
		writeStreamChunk(w, "[DONE]")
		// A completed SSE reply does not require the server to close the socket.
		<-r.Context().Done()
	})
	var deltas []string
	result, err := client.CompleteStream(ctx, "model", nil, nil, func(delta string) {
		deltas = append(deltas, delta)
		if delta == "First" {
			close(firstDelta)
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := conversation.TextValue(result.Messages[0]); got != "First second" {
		t.Errorf("text = %q", got)
	}
	if !reflect.DeepEqual(deltas, []string{"First", " second"}) {
		t.Errorf("deltas = %#v", deltas)
	}
}

func TestCompleteStreamRejectsFailures(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			"provider error",
			[]string{`{"error":{"message":"upstream disconnected"}}`},
			"upstream disconnected",
		},
		{"invalid JSON", []string{`{"choices":`}, "decode chunk"},
		{
			"missing done",
			[]string{`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`},
			"missing [DONE]",
		},
		{"missing finish reason", []string{"[DONE]"}, "missing finish reason"},
		{"transport EOF", nil, "missing [DONE]"},
		{
			"unsupported tools",
			[]string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"custom"}]}}]}`,
			},
			"unsupported tool call",
		},
		{
			"negative tool index",
			[]string{`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":-1}]}}]}`},
			"negative tool call index",
		},
		{
			"incomplete tool",
			[]string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				"[DONE]",
			},
			"incomplete tool call",
		},
		{
			"content after finish",
			[]string{
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[{"index":0,"delta":{"content":"extra"}}]}`,
				"[DONE]",
			},
			"received choice after finish",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newStreamTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				writeStreamChunk(w, `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`)
				for _, chunk := range tt.chunks {
					writeStreamChunk(w, chunk)
				}
			})
			var deltas []string
			result, err := client.CompleteStream(
				context.Background(),
				"model",
				nil,
				nil,
				func(delta string) {
					deltas = append(deltas, delta)
				},
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if !reflect.DeepEqual(result, agent.ModelClientResult{}) {
				t.Errorf("failed stream returned partial result: %#v", result)
			}
			if !reflect.DeepEqual(deltas, []string{"partial"}) {
				t.Errorf("deltas = %#v", deltas)
			}
			if strings.HasPrefix(tt.want, "missing") && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("error = %v, want unexpected EOF", err)
			}
		})
	}
}

func TestCompleteStreamRejectsExplicitErrorEvent(t *testing.T) {
	client := newStreamTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeStreamChunk(
			w,
			`{"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"stop"}]}`,
		)
		_, _ = io.WriteString(w, "event: error\ndata: {\"message\":\"generation failed\"}\n\n")
		writeStreamChunk(w, "[DONE]")
	})
	result, err := client.CompleteStream(context.Background(), "model", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "generation failed") ||
		len(result.Messages) != 0 {
		t.Fatalf("explicit SSE error returned result=%#v err=%v", result, err)
	}
}

func TestCompleteStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := newStreamTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeStreamChunk(w, `{"choices":[{"index":0,"delta":{"content":"first"}}]}`)
		<-r.Context().Done()
	})
	result, err := client.CompleteStream(ctx, "model", nil, nil, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(result, agent.ModelClientResult{}) {
		t.Errorf("cancelled stream returned partial result: %#v", result)
	}
}

func TestCompleteStreamNilCallback(t *testing.T) {
	client := newStreamTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeStreamChunk(
			w,
			`{"choices":[{"index":0,"delta":{"content":"text"},"finish_reason":"stop"}]}`,
		)
		writeStreamChunk(w, "[DONE]")
	})
	result, err := client.CompleteStream(context.Background(), "model", nil, nil, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := conversation.TextValue(result.Messages[0]); got != "text" {
		t.Errorf("text = %q", got)
	}
}

func TestCompleteStreamRejectsInvalidModel(t *testing.T) {
	client, err := NewClient("https://example.invalid", "test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CompleteStream(context.Background(), " ", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "model name is required") {
		t.Errorf("error = %v", err)
	}
}

func newStreamTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL+"/v1/", "test-key", nil, server.Client().Transport)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeStreamChunk(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}
