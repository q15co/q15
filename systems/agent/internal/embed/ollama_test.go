package embed

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/api/embed" || body.Truncate == nil || *body.Truncate ||
			body.Model != "embeddinggemma-2:740m" ||
			body.Dimensions != 3 ||
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
	embedder, err := NewOllamaEmbedder(server.URL, "embeddinggemma-2:740m", 3, 2)
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
	embedder, err := NewOllamaEmbedder(server.URL, "fixture", 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = embedder.EmbedQuery(t.Context(), "private source")
	if err == nil || err.Error() != "ollama embedding endpoint returned HTTP 400" {
		t.Fatalf("unexpected safe failure: %v", err)
	}
}
