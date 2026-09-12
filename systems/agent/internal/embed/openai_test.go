package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type capturedOpenAIRequest struct {
	Path string
	Auth string
	Body map[string]any
}

func captureOpenAIServer(
	t *testing.T,
	respond func(body map[string]any) (int, string),
) (*httptest.Server, func() []capturedOpenAIRequest) {
	t.Helper()
	var requests []capturedOpenAIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request body %q: %v", raw, err)
			return
		}
		requests = append(requests, capturedOpenAIRequest{
			Path: r.URL.Path,
			Auth: r.Header.Get("Authorization"),
			Body: body,
		})
		status, payload := respond(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(server.Close)
	return server, func() []capturedOpenAIRequest { return requests }
}

func openAIEmbeddingsJSON(count int, dims int, reverse bool) string {
	entries := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		vector := make([]float32, dims)
		for j := range vector {
			vector[j] = float32(i*1000 + j)
		}
		entries = append(entries, map[string]any{"index": i, "embedding": vector})
	}
	if reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	payload, err := json.Marshal(map[string]any{"data": entries})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

// openAIEmbeddingsForInputs builds one entry per input text, deriving each
// vector from the text's first byte so tests can verify which input produced
// which vector. reverse returns entries in descending index order to exercise
// the embedder's index sorting.
func openAIEmbeddingsForInputs(inputs []any, dims int, reverse bool) string {
	entries := make([]map[string]any, 0, len(inputs))
	for i, input := range inputs {
		text, _ := input.(string)
		base := float32(0)
		if text != "" {
			base = float32(text[0])
		}
		vector := make([]float32, dims)
		for j := range vector {
			vector[j] = base + float32(j)
		}
		entries = append(entries, map[string]any{"index": i, "embedding": vector})
	}
	if reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	payload, err := json.Marshal(map[string]any{"data": entries})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func requestInputs(t *testing.T, body map[string]any) []any {
	t.Helper()
	inputs, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("request body input = %#v, want JSON array", body["input"])
	}
	return inputs
}

func newTestOpenAIEmbedder(
	t *testing.T,
	server *httptest.Server,
	apiKey string,
	dimensions int,
	batchSize int,
) *OpenAIEmbedder {
	t.Helper()
	embedder, err := NewOpenAIEmbedder(
		server.URL,
		apiKey,
		"text-embedding-3-small",
		dimensions,
		batchSize,
	)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	return embedder
}

func requestInputCount(t *testing.T, body map[string]any) int {
	t.Helper()
	inputs, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("request body input = %#v, want JSON array", body["input"])
	}
	return len(inputs)
}

func TestOpenAIConstructorValidation(t *testing.T) {
	if _, err := NewOpenAIEmbedder("", "", "text-embedding-3-small", 1024, 0); err == nil {
		t.Fatal("expected error for missing API key without a base URL")
	}
	if _, err := NewOpenAIEmbedder(
		"http://localhost:8080/v1",
		"",
		"text-embedding-3-small",
		1024,
		0,
	); err != nil {
		t.Fatalf("keyless local mode with a base URL should be allowed: %v", err)
	}
	if _, err := NewOpenAIEmbedder("", "key", "", 1024, 0); err == nil {
		t.Fatal("expected error for empty model")
	}
	if _, err := NewOpenAIEmbedder("", "key", "text-embedding-3-small", 0, 0); err == nil {
		t.Fatal("expected error for zero dimensions")
	}
	if _, err := NewOpenAIEmbedder("", "key", "text-embedding-3-small", -3, 0); err == nil {
		t.Fatal("expected error for negative dimensions")
	}
	if _, err := NewOpenAIEmbedder("", "key", "text-embedding-3-small", 1024, -1); err == nil {
		t.Fatal("expected error for negative batch size")
	}
	embedder, err := NewOpenAIEmbedder("", "key", "text-embedding-3-small", 1024, 0)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	if embedder.batchSize != openaiEmbedBatchSize {
		t.Fatalf("default batch size = %d, want %d", embedder.batchSize, openaiEmbedBatchSize)
	}
	if embedder.baseURL != defaultOpenAIBaseURL {
		t.Fatalf("default base URL = %q, want %q", embedder.baseURL, defaultOpenAIBaseURL)
	}
}

func TestOpenAIEmbedDocumentsBatchesAndSortsByIndex(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsForInputs(requestInputs(t, body), 3, true)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 3, 2)
	texts := []EmbeddingRequest{
		{Text: "a"}, {Text: "b"}, {Text: "c"}, {Text: "d"}, {Text: "e"},
	}
	vectors, err := embedder.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("EmbedDocuments returned %d vectors, want %d", len(vectors), len(texts))
	}
	seen := requests()
	if len(seen) != 3 {
		t.Fatalf("request count = %d, want 3 batches of 2/2/1", len(seen))
	}
	for i, want := range []int{2, 2, 1} {
		if got := len(requestInputs(t, seen[i].Body)); got != want {
			t.Fatalf("batch %d input count = %d, want %d", i, got, want)
		}
		if seen[i].Path != "/embeddings" {
			t.Fatalf("batch %d path = %q, want /embeddings", i, seen[i].Path)
		}
	}
	// The server returned each batch with entries in reverse index order; the
	// embedder must sort by index so vectors line up with input order.
	for i, req := range texts {
		base := float32(req.Text[0])
		want := []float32{base, base + 1, base + 2}
		if !reflect.DeepEqual(vectors[i], want) {
			t.Fatalf("vector %d = %v, want %v", i, vectors[i], want)
		}
	}
}

func TestOpenAIEmbedDocumentsSendsDimensionsAndBearerToken(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 4, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "secret-key", 4, 0)
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(seen))
	}
	req := seen[0]
	if req.Auth != "Bearer secret-key" {
		t.Fatalf("authorization header = %q, want %q", req.Auth, "Bearer secret-key")
	}
	if req.Body["model"] != "text-embedding-3-small" {
		t.Fatalf("model = %#v, want text-embedding-3-small", req.Body["model"])
	}
	if req.Body["encoding_format"] != "float" {
		t.Fatalf("encoding_format = %#v, want float", req.Body["encoding_format"])
	}
	if req.Body["dimensions"] != float64(4) {
		t.Fatalf("dimensions = %#v, want 4", req.Body["dimensions"])
	}
}

func TestOpenAIEmbedDocumentsKeylessLocalMode(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "", 2, 0)
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(seen))
	}
	if seen[0].Auth != "" {
		t.Fatalf(
			"authorization header = %q, want empty for keyless local mode",
			seen[0].Auth,
		)
	}
}

func TestOpenAIEmbedDocumentsOmitsDimensionsWhenUnset(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 2, false)
	})
	embedder := &OpenAIEmbedder{
		client:    server.Client(),
		baseURL:   server.URL,
		model:     "custom-model",
		batchSize: 2,
	}
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(seen))
	}
	if _, ok := seen[0].Body["dimensions"]; ok {
		t.Fatalf("dimensions key present (%#v), want omitted", seen[0].Body["dimensions"])
	}
}

func TestOpenAIEmbedDocumentsRetriesOn429(t *testing.T) {
	attempts := 0
	server, _ := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		attempts++
		if attempts == 1 {
			return http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`
		}
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	embedder.retryDelays = []time.Duration{time.Millisecond}
	vectors, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	)
	if err != nil {
		t.Fatalf("EmbedDocuments after retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (429 then success)", attempts)
	}
	if len(vectors) != 1 {
		t.Fatalf("EmbedDocuments returned %d vectors, want 1", len(vectors))
	}
}

func TestOpenAIEmbedDocumentsNonRetriableStatus(t *testing.T) {
	attempts := 0
	server, _ := captureOpenAIServer(t, func(_ map[string]any) (int, string) {
		attempts++
		return http.StatusBadRequest, `{"error":{"message":"bad request"}}`
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	embedder.retryDelays = []time.Duration{time.Millisecond}
	_, err := embedder.EmbedDocuments(context.Background(), []EmbeddingRequest{{Text: "hello"}})
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on 400)", attempts)
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %q, want it to mention status 400", err)
	}
}

func TestOpenAIEmbedDocumentsCountMismatch(t *testing.T) {
	server, _ := captureOpenAIServer(t, func(_ map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(1, 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	_, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "one"}, {Text: "two"}},
	)
	if err == nil {
		t.Fatal("expected count mismatch error")
	}
	if !strings.Contains(err.Error(), "returned 1 embeddings for 2 inputs") {
		t.Fatalf("error = %q, want count mismatch", err)
	}
}

func TestOpenAIEmbedDocumentsEmptyEmbedding(t *testing.T) {
	server, _ := captureOpenAIServer(t, func(_ map[string]any) (int, string) {
		return http.StatusOK, `{"data":[{"index":0,"embedding":[]}]}`
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	_, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	)
	if err == nil {
		t.Fatal("expected empty embedding error")
	}
	if !strings.Contains(err.Error(), "empty embedding at index 0") {
		t.Fatalf("error = %q, want empty embedding at index 0", err)
	}
}

func TestOpenAIEmbedDocumentsDimensionMismatch(t *testing.T) {
	server, _ := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 3, 0)
	_, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "hello"}},
	)
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !strings.Contains(err.Error(), "want 3") {
		t.Fatalf("error = %q, want dimension mismatch", err)
	}
}

func TestOpenAIEmbedQuery(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(body map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(requestInputCount(t, body), 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	vector, err := embedder.EmbedQuery(context.Background(), "  hello  ")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if want := []float32{0, 1}; !reflect.DeepEqual(vector, want) {
		t.Fatalf("EmbedQuery vector = %v, want %v", vector, want)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(seen))
	}
	inputs, ok := seen[0].Body["input"].([]any)
	if !ok || len(inputs) != 1 || inputs[0] != "hello" {
		t.Fatalf("query input = %#v, want [hello] (trimmed)", seen[0].Body["input"])
	}
}

func TestOpenAIRejectsEmptyText(t *testing.T) {
	server, requests := captureOpenAIServer(t, func(_ map[string]any) (int, string) {
		return http.StatusOK, openAIEmbeddingsJSON(1, 2, false)
	})
	embedder := newTestOpenAIEmbedder(t, server, "key", 2, 0)
	if _, err := embedder.EmbedQuery(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty query text")
	} else if !strings.Contains(err.Error(), "query text is required") {
		t.Fatalf("error = %q, want query text is required", err)
	}
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]EmbeddingRequest{{Text: "  "}},
	); err == nil {
		t.Fatal("expected error for empty document text")
	} else if !strings.Contains(err.Error(), "document text is required") {
		t.Fatalf("error = %q, want document text is required", err)
	}
	if len(requests()) != 0 {
		t.Fatalf("request count = %d, want 0 for rejected empty text", len(requests()))
	}
}

func TestOpenAIEmbedRetryDelayCaps(t *testing.T) {
	embedder, err := NewOpenAIEmbedder("", "key", "model", 1, 0)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	if got, want := embedder.retryDelay(0), 5*time.Second; got != want {
		t.Fatalf("first delay = %s, want %s", got, want)
	}
	if got, want := embedder.retryDelay(99), 60*time.Second; got != want {
		t.Fatalf("capped delay = %s, want %s", got, want)
	}
}
