package dump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type streamingModelClient struct {
	fakeModelClient
	streamCalls int
}

func (f *streamingModelClient) CompleteStream(
	_ context.Context,
	_ string,
	_ []conversation.Message,
	_ []agent.ToolDefinition,
	onDelta func(string),
) (agent.ModelClientResult, error) {
	f.streamCalls++
	if onDelta != nil {
		onDelta("hel")
		onDelta("lo")
	}
	return f.result, f.err
}

func TestCanonicalDumpPreservesStreamingAndErrors(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, streamErr := range []error{nil, context.Canceled} {
			var buf bytes.Buffer
			var writer io.Writer
			if enabled {
				writer = &buf
			}
			inner := &streamingModelClient{fakeModelClient: fakeModelClient{
				result: agent.ModelClientResult{
					Messages: []conversation.Message{
						conversation.AssistantMessage(conversation.Text("hello", "")),
					},
					Usage: agent.ModelUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
				},
				err: streamErr,
			}}
			client := NewModelClientDump(inner, writer)
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
			if err != streamErr || !reflect.DeepEqual(result, inner.result) {
				t.Fatalf(
					"CompleteStream() = %#v, %v; want %#v, %v",
					result,
					err,
					inner.result,
					streamErr,
				)
			}
			if inner.calls != 0 || inner.streamCalls != 1 {
				t.Fatalf("batch calls = %d, streaming calls = %d", inner.calls, inner.streamCalls)
			}
			if want := []string{"hel", "lo"}; !reflect.DeepEqual(deltas, want) {
				t.Fatalf("deltas = %#v, want %#v", deltas, want)
			}
			entries := parseJSONL(t, &buf)
			if !enabled {
				if len(entries) != 0 {
					t.Fatalf("nil writer captured entries: %#v", entries)
				}
				continue
			}
			if len(entries) != 2 || entries[0]["type"] != "canonical_request" {
				t.Fatalf("unexpected entries: %#v", entries)
			}
			wantType := "canonical_response"
			if streamErr != nil {
				wantType = "canonical_error"
			}
			if entries[1]["type"] != wantType {
				t.Fatalf("entry type = %v, want %s", entries[1]["type"], wantType)
			}
		}
	}
}

func TestCanonicalDumpStreamingFallsBackToBatch(t *testing.T) {
	var buf bytes.Buffer
	inner := &fakeModelClient{result: agent.ModelClientResult{FinishReason: "stop"}}
	client := NewModelClientDump(inner, &buf)
	result, err := client.CompleteStream(context.Background(), "model", nil, nil, func(string) {
		t.Fatal("batch client emitted a delta")
	})
	if err != nil || result.FinishReason != "stop" || inner.calls != 1 {
		t.Fatalf("CompleteStream() = %#v, %v; batch calls = %d", result, err, inner.calls)
	}
	if entries := parseJSONL(t, &buf); len(entries) != 2 {
		t.Fatalf("expected canonical request and response; got %#v", entries)
	}
}

type trackedBody struct {
	io.Reader
	reads    int
	closes   int
	closeErr error
}

func (b *trackedBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *trackedBody) Close() error {
	b.closes++
	return b.closeErr
}

func TestWireDumpReadsResponseLazilyAndCapturesOnEOF(t *testing.T) {
	var buf bytes.Buffer
	body := &trackedBody{Reader: strings.NewReader("hello")}
	transport := NewTransportDump(&fakeRoundTripper{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
	}}, &buf)
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://example.com",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.reads != 0 || body.closes != 0 {
		t.Fatalf("RoundTrip consumed response: reads = %d, closes = %d", body.reads, body.closes)
	}
	if entries := parseJSONL(t, &buf); len(entries) != 1 || entries[0]["type"] != "wire_request" {
		t.Fatalf("response recorded before being read: %#v", entries)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil || string(content) != "hello" {
		t.Fatalf("ReadAll() = %q, %v", content, err)
	}
	entries := parseJSONL(t, &buf)
	if len(entries) != 1 || entries[0]["body"] != "hello" || entries[0]["body_complete"] != true {
		t.Fatalf("unexpected EOF capture: %#v", entries)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if body.closes != 1 || buf.Len() != 0 {
		t.Fatalf(
			"Close() emitted duplicate capture or failed to close body: closes = %d, log = %s",
			body.closes,
			&buf,
		)
	}
}

func TestWireDumpCapturesPartialBodyAndPreservesCloseError(t *testing.T) {
	var buf bytes.Buffer
	closeErr := errors.New("close failed")
	body := &trackedBody{Reader: strings.NewReader("hello"), closeErr: closeErr}
	capture := &captureBody{
		inner:  body,
		writer: newLineWriter(&buf),
		entry:  map[string]any{"type": "wire_response"},
	}
	prefix := make([]byte, 2)
	if n, err := capture.Read(prefix); n != 2 || err != nil {
		t.Fatalf("Read() = %d, %v", n, err)
	}
	if err := capture.Close(); err != closeErr {
		t.Fatalf("Close() = %v, want %v", err, closeErr)
	}
	if body.reads != 1 {
		t.Fatalf("Close() drained response body: reads = %d", body.reads)
	}
	entries := parseJSONL(t, &buf)
	if len(entries) != 1 || entries[0]["body"] != "he" || entries[0]["body_complete"] != false ||
		entries[0]["body_error"] != closeErr.Error() {
		t.Fatalf("unexpected partial capture: %#v", entries)
	}
}

type failedBody struct{}

func (failedBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), io.ErrUnexpectedEOF
}

func (failedBody) Close() error { return nil }

func TestWireDumpPreservesReadErrorAndPartialData(t *testing.T) {
	var buf bytes.Buffer
	capture := &captureBody{
		inner:  failedBody{},
		writer: newLineWriter(&buf),
		entry:  map[string]any{"type": "wire_response"},
	}
	content, err := io.ReadAll(capture)
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(content) != "partial" {
		t.Fatalf("ReadAll() = %q, %v; want partial, unexpected EOF", content, err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	entries := parseJSONL(t, &buf)
	if len(entries) != 1 || entries[0]["body"] != "partial" ||
		entries[0]["body_error"] != io.ErrUnexpectedEOF.Error() {
		t.Fatalf("unexpected failed capture: %#v", entries)
	}
}

func TestWireDumpBoundsCaptureWithoutTruncatingCallerResponse(t *testing.T) {
	var buf bytes.Buffer
	size := int64(maxCapturedBodyBytes + 1)
	capture := &captureBody{
		inner:  io.NopCloser(strings.NewReader(strings.Repeat("x", int(size)))),
		writer: newLineWriter(&buf),
		entry:  map[string]any{"type": "wire_response"},
	}
	count, err := io.Copy(io.Discard, capture)
	if err != nil || count != size {
		t.Fatalf("Copy() = %d, %v; want %d, nil", count, err, size)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	entries := parseJSONL(t, &buf)
	if len(entries) != 1 || entries[0]["body_truncated"] != true ||
		entries[0]["body_bytes"] != float64(size) {
		t.Fatalf("missing capture truncation metadata")
	}
	if got := len(mustGet[string](t, entries[0], "body")); got != maxCapturedBodyBytes {
		t.Fatalf("captured %d bytes, want %d", got, maxCapturedBodyBytes)
	}
}
