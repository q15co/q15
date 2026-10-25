package embed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaEmbeddingsNeverSilentlyTruncate(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
			Truncate   *bool    `json:"truncate"`
			Options    struct {
				NumBatch int `json:"num_batch"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/api/embed" || body.Truncate == nil || *body.Truncate ||
			body.Model != "embeddinggemma-2:740m" ||
			body.Dimensions != 3 ||
			body.Options.NumBatch != 8192 ||
			len(body.Input) > 2 {
			t.Errorf("invalid native embedding request: %+v", body)
		}
		vectors := make([][]float32, len(body.Input))
		for n := range vectors {
			vectors[n] = []float32{1, 0, 0}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	embedder, err := NewOllamaEmbedder(server.URL, "embeddinggemma-2:740m", 3, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embedder.EmbedDocuments(
		t.Context(),
		[]EmbeddingRequest{{Text: "one"}, {Text: "two"}, {Text: "three"}},
	)
	if err != nil || len(vectors) != 3 || calls != 2 {
		t.Fatalf("batching: vectors=%d calls=%d error=%v", len(vectors), calls, err)
	}
}

func TestOllamaEmbeddingFailureDoesNotExposeSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "private source echoed by provider")
	}))
	defer server.Close()
	embedder, err := NewOllamaEmbedder(server.URL, "fixture", 3, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = embedder.EmbedQuery(t.Context(), "private source")
	if err == nil || err.Error() != "ollama embedding endpoint returned HTTP 400" {
		t.Fatalf("unexpected safe failure: %v", err)
	}
}

func TestOllamaEmbeddingTokenBatchOverride(t *testing.T) {
	source := strings.Repeat("0x1234 ", 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input    []string `json:"input"`
			Truncate *bool    `json:"truncate"`
			Options  struct {
				NumBatch int `json:"num_batch"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Options.NumBatch != 16384 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if len(body.Input) != 1 || body.Input[0] != source || body.Truncate == nil ||
			*body.Truncate {
			t.Error("source changed or truncation enabled")
		}
		fmt.Fprint(w, `{"embeddings":[[1,0,0]]}`)
	}))
	defer server.Close()
	embedder, err := NewEmbedder(t.Context(), Settings{
		Provider: ProviderOllama, BaseURL: server.URL, Model: "fixture",
		Dimensions: 3, BatchSize: 1, BatchTokens: 16384,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.EmbedQuery(t.Context(), source); err != nil {
		t.Fatal(err)
	}
}
