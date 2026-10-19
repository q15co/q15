package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/fileops"
)

func TestEmbeddingToolUsesConfiguredModelWithoutCatalogSelection(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected endpoint or authentication", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- request.Model
		// Stop before vector storage so this test needs only the embedding endpoint.
		http.Error(w, "binding-test-response", http.StatusBadRequest)
	}))
	defer server.Close()
	rt := config.AgentRuntime{}
	rt.Tools.Embeddings = config.EmbeddingsToolRuntime{
		Enabled:    true,
		Provider:   embed.ProviderOpenAI,
		BaseURL:    server.URL,
		Model:      "Qwen/Qwen3-Embedding-0.6B",
		Dimensions: 1024,
		QdrantURL:  "http://127.0.0.1:6333",
	}
	service, err := newEmbeddingService(
		t.Context(),
		rt,
		fileops.Settings{WorkspaceLocalDir: t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	_, err = service.Search(t.Context(), embed.SearchOptions{Query: "test binding", Mode: "dense"})
	if err == nil || !strings.Contains(err.Error(), "binding-test-response") {
		t.Fatalf("embedding endpoint error = %v", err)
	}
	select {
	case model := <-requests:
		if model != rt.Tools.Embeddings.Model {
			t.Fatalf("dispatched model = %q, want %q", model, rt.Tools.Embeddings.Model)
		}
	default:
		t.Fatal("configured embedding endpoint was not called")
	}
}
