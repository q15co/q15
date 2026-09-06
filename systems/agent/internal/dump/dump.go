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
var _ agent.StreamingModelClient = (*ModelClientDump)(nil)

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
	return d.complete(model, messages, tools, func() (agent.ModelClientResult, error) {
		return d.inner.Complete(ctx, model, messages, tools)
	})
}

// CompleteStream preserves optional streaming while capturing the canonical
// request and final response. Batch-only clients complete without emitting deltas.
func (d *ModelClientDump) CompleteStream(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	onDelta func(string),
) (agent.ModelClientResult, error) {
	streaming, ok := d.inner.(agent.StreamingModelClient)
	if !ok {
		return d.Complete(ctx, model, messages, tools)
	}
	return d.complete(model, messages, tools, func() (agent.ModelClientResult, error) {
		return streaming.CompleteStream(ctx, model, messages, tools, onDelta)
	})
}

func (d *ModelClientDump) complete(
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	complete func() (agent.ModelClientResult, error),
) (agent.ModelClientResult, error) {
	if d.writer == nil {
		return complete()
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

	result, err := complete()
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
		"usage":         result.Usage,
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
// response (or error) entries. Responses are captured as callers read them and
// recorded at EOF, a read error, or Close, so streaming remains incremental.
func (t *TransportDump) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.writer == nil {
		return t.inner.RoundTrip(req)
	}

	var reqBody any
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			log.Printf("q15: dump: read request body: %v", err)
		}
		reqBody = bodyField(raw)
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

	entry := map[string]any{
		"type":      "wire_response",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"method":    req.Method,
		"url":       req.URL.String(),
		"status":    resp.StatusCode,
	}
	if resp.Body == nil {
		entry["body"] = nil
		t.writer.writeJSON(entry)
	} else {
		resp.Body = &captureBody{inner: resp.Body, writer: t.writer, entry: entry}
	}

	return resp, nil
}

// Keep debugging enabled without retaining an unbounded streaming response.
const maxCapturedBodyBytes = 8 * 1024 * 1024

type captureBody struct {
	inner  io.ReadCloser
	writer *lineWriter
	entry  map[string]any

	mu        sync.Mutex
	buffer    bytes.Buffer
	bytesRead int64
	finished  bool
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.finished {
		b.bytesRead += int64(n)
		_, _ = b.buffer.Write(p[:min(n, maxCapturedBodyBytes-b.buffer.Len())])
		if err != nil {
			b.finish(err == io.EOF, err)
		}
	}
	return n, err
}

func (b *captureBody) Close() error {
	// Closing the underlying body must be able to unblock a concurrent Read.
	err := b.inner.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finish(false, err)
	return err
}

// finish runs with mu held. It emits at most one entry, including partial bodies
// when callers close early or the underlying stream fails.
func (b *captureBody) finish(complete bool, err error) {
	if b.finished {
		return
	}
	b.finished = true
	b.entry["body"] = bodyField(b.buffer.Bytes())
	b.entry["body_complete"] = complete
	if b.bytesRead > maxCapturedBodyBytes {
		b.entry["body_truncated"] = true
		b.entry["body_bytes"] = b.bytesRead
	}
	if err != nil && err != io.EOF {
		b.entry["body_error"] = err.Error()
	}
	b.writer.writeJSON(b.entry)
	b.buffer.Reset()
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
		log.Printf("q15: dump: marshal entry: %v", err)
		return
	}
	lw.mu.Lock()
	defer lw.mu.Unlock()
	fmt.Fprintf(lw.w, "%s\n", data)
}

// bodyField encodes raw HTTP body bytes as a jq-native JSON value when
// possible, so dumped entries stay navigable without fromjson:
//   - A single JSON value (typical request body, non-streaming response) is
//     returned as json.RawMessage and embeds as a nested object/array.
//   - Newline-delimited JSON (streaming responses, e.g. Ollama /api/chat) is
//     decoded into a JSON array so each chunk remains jq-addressable.
//   - Anything else (non-JSON bytes) falls back to a string so the entry is
//     still emitted rather than dropped on a marshal error.
func bodyField(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	if lines := decodeNDJSON(raw); lines != nil {
		return lines
	}
	return string(raw)
}

// decodeNDJSON decodes newline-delimited JSON into a slice. It returns nil if
// the bytes are not clean NDJSON with more than one value; single values are
// handled by the json.Valid path in bodyField.
func decodeNDJSON(raw []byte) []any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return nil
		}
		out = append(out, v)
	}
	if len(out) > 1 {
		return out
	}
	return nil
}
