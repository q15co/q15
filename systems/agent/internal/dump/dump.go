// Package dump provides optional payload capture for debugging and live demos.
// It wraps the agent.ModelClient interface (canonical request/response) and
// the http.RoundTripper interface (raw wire request/response), writing JSONL
// entries to an io.Writer. When the writer is nil, all wrappers are no-ops.
package dump

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// --- canonical ModelClient decorator ---

// ModelClientDump wraps an agent.ModelClient and writes JSONL entries for each
// canonical request and response. A nil writer makes the wrapper a transparent
// pass-through.
type ModelClientDump struct {
	inner  agent.ModelClient
	writer *lineWriter
}

var _ agent.ModelClient = (*ModelClientDump)(nil)

// NewModelClientDump wraps inner with an optional JSONL writer. If writer is
// nil, the returned wrapper is a no-op pass-through.
func NewModelClientDump(inner agent.ModelClient, writer io.Writer) *ModelClientDump {
	return &ModelClientDump{inner: inner, writer: newLineWriter(writer)}
}

// Complete delegates to the inner ModelClient and writes canonical request and
// response (or error) entries.
func (d *ModelClientDump) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	if d.writer == nil {
		return d.inner.Complete(ctx, model, messages, tools)
	}

	d.writer.writeJSON(map[string]any{
		"type":          "canonical_request",
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"model":         model,
		"message_count": len(messages),
		"tool_count":    len(tools),
		"messages":      messages,
		"tools":         tools,
	})

	result, err := d.inner.Complete(ctx, model, messages, tools)
	if err != nil {
		d.writer.writeJSON(map[string]any{
			"type":      "canonical_error",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"model":     model,
			"error":     err.Error(),
		})
		return result, err
	}

	d.writer.writeJSON(map[string]any{
		"type":          "canonical_response",
		"timestamp":     time.Now().UTC().Format(time.RFC3339Nano),
		"model":         model,
		"finish_reason": result.FinishReason,
		"message_count": len(result.Messages),
		"messages":      result.Messages,
	})
	return result, nil
}

// --- raw wire RoundTripper wrapper ---

// TransportDump wraps an http.RoundTripper and writes JSONL entries for each
// raw HTTP request and response. A nil writer makes the wrapper a transparent
// pass-through.
type TransportDump struct {
	inner  http.RoundTripper
	writer *lineWriter
}

// NewTransportDump wraps inner with an optional JSONL writer. If writer is nil,
// the returned wrapper is a no-op pass-through.
func NewTransportDump(inner http.RoundTripper, writer io.Writer) *TransportDump {
	return &TransportDump{inner: inner, writer: newLineWriter(writer)}
}

// RoundTrip delegates to the inner transport and writes wire request and
// response (or error) entries. The request and response bodies are fully
// captured and restored so callers see no behavioural difference.
func (t *TransportDump) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.writer == nil {
		return t.inner.RoundTrip(req)
	}

	var reqBody json.RawMessage
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			log.Printf("q15: dump: read request body: %v", err)
		}
		reqBody = json.RawMessage(raw)
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}

	t.writer.writeJSON(map[string]any{
		"type":      "wire_request",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"method":    req.Method,
		"url":       req.URL.String(),
		"body":      reqBody,
	})

	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		t.writer.writeJSON(map[string]any{
			"type":      "wire_error",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"method":    req.Method,
			"url":       req.URL.String(),
			"error":     err.Error(),
		})
		return nil, err
	}

	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Printf("q15: dump: read response body: %v", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	t.writer.writeJSON(map[string]any{
		"type":      "wire_response",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"method":    req.Method,
		"url":       req.URL.String(),
		"status":    resp.StatusCode,
		"body":      json.RawMessage(raw),
	})

	return resp, nil
}

// --- shared line writer with mutex ---

// lineWriter serialises JSONL writes so concurrent model calls do not
// interleave or corrupt lines.
type lineWriter struct {
	w  io.Writer
	mu sync.Mutex
}

func newLineWriter(w io.Writer) *lineWriter {
	if w == nil {
		return nil
	}
	return &lineWriter{w: w}
}

func (lw *lineWriter) writeJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	lw.mu.Lock()
	defer lw.mu.Unlock()
	fmt.Fprintf(lw.w, "%s\n", data)
}
