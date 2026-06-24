package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestOllamaClient_DiscoverSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/incorrect Authorization header: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(ollamaTagsResponse{
			Models: []ollamaTagsModel{
				{Name: "kimi-k2:cloud"},
				{Name: "llama3:latest"},
			},
		})
	})
	showCalls := 0
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		showCalls++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing Authorization on /api/show")
		}
		var req ollamaShowRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Name {
		case "kimi-k2:cloud":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"capabilities": []string{"completion", "tools", "vision"},
				"model_info":   map[string]int64{"general.parameter_count": 1042000000000},
			})
		case "llama3:latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"capabilities": []string{"completion"},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "ollama",
		BaseURL: server.URL,
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if showCalls != 2 {
		t.Fatalf("expected 2 /api/show calls, got %d", showCalls)
	}

	byID := map[string]modelcatalog.Model{}
	for _, m := range models {
		byID[m.ProviderModel] = m
	}
	kimi := byID["kimi-k2:cloud"]
	if !kimi.Capabilities.Text || !kimi.Capabilities.ToolCalling || !kimi.Capabilities.ImageInput {
		t.Errorf("kimi capabilities wrong: %+v", kimi.Capabilities)
	}
	if kimi.Capabilities.Reasoning {
		t.Errorf("kimi should not have reasoning, got %+v", kimi.Capabilities)
	}
	if kimi.Source != modelcatalog.SourceOllama {
		t.Errorf("source = %q, want %q", kimi.Source, modelcatalog.SourceOllama)
	}
	if kimi.ParameterCount != 1042000000000 {
		t.Errorf("parameter count = %d, want 1042000000000", kimi.ParameterCount)
	}
	if models[1].ProviderModel == "llama3:latest" && models[1].ParameterCount != 0 {
		t.Errorf("llama3 parameter count should be 0, got %d", models[1].ParameterCount)
	}
}

func TestOllamaClient_ShowErrorFallbackToTextOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaTagsResponse{
			Models: []ollamaTagsModel{{Name: "mystery-model:latest"}},
		})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "ollama",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if !models[0].Capabilities.Text {
		t.Errorf("expected Text-only fallback, got %+v", models[0].Capabilities)
	}
	if models[0].Capabilities.ToolCalling || models[0].Capabilities.ImageInput {
		t.Errorf("expected only Text, got %+v", models[0].Capabilities)
	}
}

func TestOllamaClient_EmptyTags(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaTagsResponse{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	models, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "ollama",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(models))
	}
}

func TestOllamaClient_TagsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewRosterClient(server.Client())
	_, err := client.Discover(context.Background(), modelcatalog.Provider{
		Type:    "ollama",
		BaseURL: server.URL,
	})
	if err == nil {
		t.Fatal("expected error from /api/tags failure")
	}
}

func TestCapabilitiesFromOllamaShow(t *testing.T) {
	tests := []struct {
		name string
		caps []string
		want modelcatalog.Capabilities
	}{
		{"all", []string{"completion", "tools", "vision", "thinking", "audio"},
			modelcatalog.Capabilities{
				Text:        true,
				ToolCalling: true,
				ImageInput:  true,
				Reasoning:   true,
				AudioInput:  true,
			}},
		{"unknown-ignored", []string{"completion", "embeddings", "unknown-cap"},
			modelcatalog.Capabilities{Text: true}},
		{"empty", nil, modelcatalog.Capabilities{Text: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilitiesFromOllamaShow(tc.caps)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
