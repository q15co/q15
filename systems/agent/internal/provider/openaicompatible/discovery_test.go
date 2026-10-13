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
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
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

// The roster is a relative path joined onto the configured API root, so a base
// URL that already carries a versioned path is asked for that path plus
// /models. Appending a version segment here would ask for /v1/v1/models on a
// base like https://api.moonshot.ai/v1 and 404 every request.
func TestOpenAIClient_DiscoverHonoursBaseURLPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/models")
		}
		_ = json.NewEncoder(w).Encode(openaiModelsResponse{
			Data: []openaiModelsEntry{{ID: "deepseek-ai/DeepSeek-V4-Flash"}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "openai-compatible",
		BaseURL: server.URL + "/v1",
		APIKey:  "secret-key",
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 1 || models[0].ProviderModel != "deepseek-ai/DeepSeek-V4-Flash" {
		t.Fatalf("models = %+v, want the single model the API root publishes", models)
	}
}

func TestOpenAIClient_AuthError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
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
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
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

func TestOpenAIClient_DiscoverFiltersNonChatModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-oss-20b","metadata":{"tags":["chat","reasoning"]}},
			{"id":"BAAI/bge-m3","metadata":{"tags":["embed"]}},
			{"id":"flux","metadata":{"tags":["image-gen"]}},
			{"id":"speech","metadata":{"tags":["tts"]}},
			{"id":"transcribe","metadata":{"tags":["stt"]}},
			{"id":"video","metadata":{"tags":["video-gen"]}},
			{"id":"plain-openai-model"},
			{"id":"unknown-tags","metadata":{"tags":["custom-tag"]}}
		]}`))
	}))
	defer server.Close()
	models, err := NewRosterClient(
		server.Client(),
	).Discover(context.Background(), modelcatalog.Provider{BaseURL: server.URL + "/v1/openai/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].ProviderModel != "openai/gpt-oss-20b" ||
		models[1].ProviderModel != "plain-openai-model" || models[2].ProviderModel != "unknown-tags" {
		t.Fatalf("roster = %+v, want chat and unclassified models only", models)
	}
}
