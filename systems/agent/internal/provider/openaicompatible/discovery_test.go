package openaicompatible

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestOpenAIClient_DiscoverSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("missing/incorrect Authorization: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(openaiModelsResponse{
			Data: []openaiModelsEntry{
				{ID: "moonshot-v1-128k"},
				{ID: "  "}, // empty after trim, should be skipped
				{ID: "moonshot-v1-32k"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "openai-compatible",
		BaseURL: server.URL,
		APIKey:  "secret-key",
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	for _, m := range models {
		if !m.Capabilities.Text {
			t.Errorf("model %q should have Text capability", m.ProviderModel)
		}
		if m.Source != modelcatalog.SourceOpenAI {
			t.Errorf("source = %q, want %q", m.Source, modelcatalog.SourceOpenAI)
		}
	}
}

func TestOpenAIClient_AuthError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	_, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "openai-compatible",
		BaseURL: server.URL,
		APIKey:  "bad-key",
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestOpenAIClient_EmptyModels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(openaiModelsResponse{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "openai-compatible",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(models))
	}
}

func TestOpenAIClient_MissingBaseURL(t *testing.T) {
	client := NewRosterClient(http.DefaultClient)
	_, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "openai-compatible",
		BaseURL: "",
	})
	if err == nil {
		t.Fatal("expected error for missing base_url")
	}
}
