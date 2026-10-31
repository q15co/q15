package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProvidersCutOversizedResponses covers the read-side ceiling: a provider
// response larger than providerResponseLimit must fail at the read, before it
// is decoded into memory, rather than being truncated after allocation.
func TestProvidersCutOversizedResponses(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, providerResponseLimit+1)
	for i := range oversized {
		oversized[i] = 'x'
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(oversized)
	}))
	t.Cleanup(server.Close)

	brave, err := NewBraveWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewBraveWebSearch() error = %v", err)
	}
	brave.baseURL = server.URL
	brave.client = server.Client()

	ollama, err := NewOllamaWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewOllamaWebSearch() error = %v", err)
	}
	ollama.baseURL = server.URL
	ollama.client = server.Client()

	providers := map[string]Provider{
		"brave":  brave,
		"ollama": ollama,
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			_, err := provider.Search(
				context.Background(),
				SearchRequest{Query: "oversized", Count: 1},
			)
			if err == nil {
				t.Fatalf("Search() error = nil, want a read-limit error")
			}
			if !strings.Contains(err.Error(), "read limit") {
				t.Fatalf("Search() error = %v, want it to name the read limit", err)
			}
		})
	}
}
