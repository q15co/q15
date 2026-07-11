package dump

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// --- helpers ---

type fakeModelClient struct {
	result agent.ModelClientResult
	err    error
	calls  int
}

func (f *fakeModelClient) Complete(
	_ context.Context,
	_ string,
	_ []conversation.Message,
	_ []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	f.calls++
	return f.result, f.err
}

type fakeRoundTripper struct {
	resp *http.Response
	err  error
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Drain and discard the request body so the caller sees it consumed.
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	return f.resp, f.err
}

func makeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// JSONL helpers

func parseJSONL(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	dec := json.NewDecoder(buf)
	var entries []map[string]any
	for dec.More() {
		var entry map[string]any
		if err := dec.Decode(&entry); err != nil {
			t.Fatalf("parse JSONL line: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func mustGet[T any](t *testing.T, m map[string]any, key string) T {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("entry missing key %q; have: %v", key, m)
	}
	tv, ok := v.(T)
	if !ok {
		t.Fatalf("entry key %q is %T, want %T", key, v, v)
	}
	return tv
}

// --- canonical decorator tests ---

func TestCanonicalDump_WritesRequestAndResponse(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeModelClient{
		result: agent.ModelClientResult{
			FinishReason: "stop",
			Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("hello", "")),
			},
		},
	}
	d := NewModelClientDump(inner, &buf)

	_, err := d.Complete(context.Background(), "test-model",
		[]conversation.Message{conversation.UserMessage("hi")},
		nil)
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	entries := parseJSONL(t, &buf)
	if len(entries) != 2 {
		t.Fatalf("expected 2 JSONL entries, got %d", len(entries))
	}

	reqEntry := entries[0]
	if mustGet[string](t, reqEntry, "type") != "canonical_request" {
		t.Errorf("entry 0 type = %q, want canonical_request", reqEntry["type"])
	}
	if mustGet[string](t, reqEntry, "model") != "test-model" {
		t.Errorf("entry 0 model = %v, want test-model", reqEntry["model"])
	}
	if mustGet[float64](t, reqEntry, "message_count") != 1 {
		t.Errorf("entry 0 message_count = %v, want 1", reqEntry["message_count"])
	}

	respEntry := entries[1]
	if mustGet[string](t, respEntry, "type") != "canonical_response" {
		t.Errorf("entry 1 type = %q, want canonical_response", respEntry["type"])
	}
	if mustGet[string](t, respEntry, "finish_reason") != "stop" {
		t.Errorf("entry 1 finish_reason = %v, want stop", respEntry["finish_reason"])
	}
}

func TestCanonicalDump_PassesThroughError(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeModelClient{err: context.DeadlineExceeded}
	d := NewModelClientDump(inner, &buf)

	_, err := d.Complete(context.Background(), "m",
		[]conversation.Message{conversation.UserMessage("hi")},
		nil)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	entries := parseJSONL(t, &buf)
	// Request entry should still be written; error entry replaces response.
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 JSONL entry, got %d", len(entries))
	}
	if mustGet[string](t, entries[0], "type") != "canonical_request" {
		t.Errorf("entry 0 type = %q, want canonical_request", entries[0]["type"])
	}
	// Look for an error entry.
	foundErr := false
	for _, e := range entries {
		if e["type"] == "canonical_error" {
			foundErr = true
			if !strings.Contains(
				strings.ToLower(mustGet[string](t, e, "error")),
				"deadline exceeded",
			) {
				t.Errorf("error entry missing error text, got %v", e["error"])
			}
		}
	}
	if !foundErr {
		t.Error("expected a canonical_error entry")
	}
}

func TestCanonicalDump_NilWriterIsNoOp(t *testing.T) {
	inner := &fakeModelClient{
		result: agent.ModelClientResult{FinishReason: "stop"},
	}
	d := NewModelClientDump(inner, nil)

	_, err := d.Complete(context.Background(), "m",
		[]conversation.Message{conversation.UserMessage("hi")},
		nil)
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner called %d times, want 1", inner.calls)
	}
}

func TestCanonicalDump_IncludesToolCount(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeModelClient{
		result: agent.ModelClientResult{FinishReason: "stop"},
	}
	d := NewModelClientDump(inner, &buf)

	tools := []agent.ToolDefinition{
		{Name: "foo", Description: "does foo"},
		{Name: "bar", Description: "does bar"},
	}
	_, err := d.Complete(context.Background(), "m",
		[]conversation.Message{conversation.UserMessage("hi")},
		tools)
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	entries := parseJSONL(t, &buf)
	reqEntry := entries[0]
	if mustGet[float64](t, reqEntry, "tool_count") != 2 {
		t.Errorf("tool_count = %v, want 2", reqEntry["tool_count"])
	}
}

// --- raw wire transport tests ---

func TestWireDump_CapturesRequestAndResponseBodies(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeRoundTripper{
		resp: makeResponse(200, `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`),
	}
	dt := NewTransportDump(inner, &buf)

	req, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://api.example.com/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	// Consume the response body to trigger any body-restore logic.
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(respBody) != `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}` {
		t.Errorf("response body not preserved: %q", respBody)
	}

	entries := parseJSONL(t, &buf)
	if len(entries) != 2 {
		t.Fatalf("expected 2 JSONL entries, got %d", len(entries))
	}

	reqEntry := entries[0]
	if mustGet[string](t, reqEntry, "type") != "wire_request" {
		t.Errorf("entry 0 type = %q, want wire_request", reqEntry["type"])
	}
	if mustGet[string](t, reqEntry, "method") != "POST" {
		t.Errorf("entry 0 method = %v, want POST", reqEntry["method"])
	}
	if !strings.Contains(mustGet[string](t, reqEntry, "url"), "api.example.com") {
		t.Errorf("entry 0 url = %v, want api.example.com", reqEntry["url"])
	}
	// Body is json.RawMessage — unmarshals as nested JSON, not a string.
	reqBody := reqEntry["body"]
	switch v := reqBody.(type) {
	case string:
		if !strings.Contains(v, `"model":"test"`) {
			t.Errorf("entry 0 body missing request payload, got %v", v)
		}
	case map[string]any:
		if v["model"] != "test" {
			t.Errorf("entry 0 body.model = %v, want test", v["model"])
		}
	default:
		t.Errorf("entry 0 body is %T, want string or map", reqBody)
	}

	respEntry := entries[1]
	if mustGet[string](t, respEntry, "type") != "wire_response" {
		t.Errorf("entry 1 type = %q, want wire_response", respEntry["type"])
	}
	if mustGet[float64](t, respEntry, "status") != 200 {
		t.Errorf("entry 1 status = %v, want 200", respEntry["status"])
	}
	// Body is json.RawMessage — unmarshals as nested JSON.
	respBodyField := respEntry["body"]
	switch v := respBodyField.(type) {
	case string:
		if !strings.Contains(v, `"choices"`) {
			t.Errorf("entry 1 body missing response payload, got %v", v)
		}
	case map[string]any:
		if _, ok := v["choices"]; !ok {
			t.Errorf("entry 1 body missing choices key, got %v", v)
		}
	default:
		t.Errorf("entry 1 body is %T, want string or map", respBodyField)
	}
}

func TestWireDump_PreservesResponseBodyForCaller(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeRoundTripper{
		resp: makeResponse(200, `{"ok":true}`),
	}
	dt := NewTransportDump(inner, &buf)

	req, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://api.example.com", strings.NewReader(`{"q":1}`))
	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	// Read twice to ensure body is fully reusable.
	first, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(first) != `{"ok":true}` {
		t.Fatalf("first read = %q, want {\"ok\":true}", first)
	}
}

func TestWireDump_NilWriterIsNoOp(t *testing.T) {
	inner := &fakeRoundTripper{
		resp: makeResponse(200, `{}`),
	}
	dt := NewTransportDump(inner, nil)

	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://api.example.com", nil)
	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func TestWireDump_HandlesNilRequestBody(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeRoundTripper{
		resp: makeResponse(200, `{}`),
	}
	dt := NewTransportDump(inner, &buf)

	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://api.example.com/health", nil)
	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	entries := parseJSONL(t, &buf)
	if len(entries) != 2 {
		t.Fatalf("expected 2 JSONL entries, got %d", len(entries))
	}
	// Request body should be null/nil for no body, not a crash.
	if entries[0]["body"] != nil {
		bodyStr := fmt.Sprintf("%v", entries[0]["body"])
		if bodyStr != "" && bodyStr != "null" && bodyStr != "<nil>" {
			t.Errorf("expected null/nil body for nil request body, got %v", entries[0]["body"])
		}
	}
}

func TestWireDump_CapturesTransportError(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeRoundTripper{err: io.ErrUnexpectedEOF}
	dt := NewTransportDump(inner, &buf)

	req, _ := http.NewRequestWithContext(context.Background(), "POST",
		"https://api.example.com", strings.NewReader(`{"q":1}`))
	_, err := dt.RoundTrip(req)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("expected ErrUnexpectedEOF, got %v", err)
	}

	entries := parseJSONL(t, &buf)
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 entry, got %d", len(entries))
	}
	// Should have a wire_request and a wire_error.
	foundErr := false
	for _, e := range entries {
		if e["type"] == "wire_error" {
			foundErr = true
			if !strings.Contains(
				strings.ToLower(mustGet[string](t, e, "error")),
				"unexpected eof",
			) {
				t.Errorf("wire_error missing error text, got %v", e["error"])
			}
		}
	}
	if !foundErr {
		t.Error("expected a wire_error entry")
	}
}

func TestWireDump_CapturesStreamingNDJSONResponse(t *testing.T) {
	var buf bytes.Buffer
	// Ollama /api/chat streams newline-delimited JSON objects. This body is
	// NOT a single JSON value, so a naive json.RawMessage field would fail to
	// marshal and the wire_response entry would be silently dropped.
	ndjson := `{"model":"x","message":{"role":"assistant","content":"hel"},"done":false}` + "\n" +
		`{"model":"x","message":{"role":"assistant","content":"lo"},"done":true}` + "\n"
	inner := &fakeRoundTripper{
		resp: makeResponse(200, ndjson),
	}
	dt := NewTransportDump(inner, &buf)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://ollama.com/api/chat", strings.NewReader(`{"model":"x","stream":true}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := dt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	// Caller still sees the full NDJSON body, byte-for-byte.
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != ndjson {
		t.Errorf("response body not preserved: %q", respBody)
	}

	entries := parseJSONL(t, &buf)
	var respEntry map[string]any
	for _, e := range entries {
		if e["type"] == "wire_response" {
			respEntry = e
		}
	}
	if respEntry == nil {
		t.Fatalf("wire_response entry missing; got %d entries", len(entries))
	}
	// NDJSON body decodes as a JSON array of chunks (jq-native, no fromjson).
	chunks, ok := respEntry["body"].([]any)
	if !ok {
		t.Fatalf("wire_response body is %T, want []any (decoded NDJSON)", respEntry["body"])
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 NDJSON chunks, got %d", len(chunks))
	}
	first := chunks[0].(map[string]any)
	if first["done"] != false {
		t.Errorf("chunk 0 done = %v, want false", first["done"])
	}
	second := chunks[1].(map[string]any)
	if second["done"] != true {
		t.Errorf("chunk 1 done = %v, want true", second["done"])
	}
}
