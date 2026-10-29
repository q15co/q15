package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestOllamaDiscoveryPreservesCapabilitiesWithMixedModelInfo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count any
		want  int64
	}{
		{"numeric parameter count", int64(27000000000), 27000000000},
		{"invalid parameter count", "unknown", 0},
		{"absent parameter count", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/tags" {
						_, _ = w.Write([]byte(`{"models":[{"name":"clef:27b"}]}`))
						return
					}
					info := map[string]any{
						"general.architecture":  "qwen35",
						"tokenizer.ggml.tokens": []string{"example"},
						"vendor.flags":          map[string]bool{"enabled": true},
					}
					if tc.count != nil {
						info["general.parameter_count"] = tc.count
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"capabilities": []string{"decision", "vision"}, "model_info": info,
					})
				}),
			)
			defer server.Close()
			models, err := NewRosterClient(
				server.Client(),
			).Discover(context.Background(), modelcatalog.Provider{BaseURL: server.URL})
			if err != nil || len(models) != 1 {
				t.Fatalf("roster = %+v, %v", models, err)
			}
			m := models[0]
			if !m.Capabilities.Known || !m.Capabilities.Decision || !m.Capabilities.ImageInput ||
				m.Capabilities.Chat || m.Capabilities.Provenance["decision"] != "derived" || m.ParameterCount != tc.want {
				t.Fatalf("mixed /api/show metadata lost capabilities or parameter count: %+v", m)
			}
		})
	}
}

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
	var showCalls atomic.Int32
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		showCalls.Add(1)
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
	if showCalls.Load() != 2 {
		t.Fatalf("expected 2 /api/show calls, got %d", showCalls.Load())
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

func TestOllamaDiscoveryUsesNativeContextWindow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		architecture string
		context      any
		want         int
	}{
		{"gpt oss", "gptoss", 131072, 131072},
		{"deepseek", "deepseek_v41", 1048576, 1048576},
		{"missing architecture", "", 131072, 0},
		{"missing context", "gptoss", nil, 0},
		{"invalid context", "gptoss", "unknown", 0},
		{"fractional context", "gptoss", 1.5, 0},
		{"negative context", "gptoss", -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/tags" {
						_, _ = w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
						return
					}
					info := map[string]any{
						"general.architecture":  tc.architecture,
						"tokenizer.ggml.tokens": []string{"example"},
					}
					if tc.context != nil {
						info["gptoss.context_length"] = tc.context
						if tc.architecture != "" {
							info[tc.architecture+".context_length"] = tc.context
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"capabilities": []string{"completion", "tools"}, "model_info": info,
					})
				}),
			)
			defer server.Close()
			models, err := NewRosterClient(server.Client()).Discover(
				t.Context(), modelcatalog.Provider{BaseURL: server.URL},
			)
			if err != nil || len(models) != 1 {
				t.Fatalf("roster = %+v, %v", models, err)
			}
			if models[0].MaxContextTokens != tc.want || !models[0].Capabilities.ToolCalling {
				t.Fatalf("native context = %d, want %d; tools = %v",
					models[0].MaxContextTokens, tc.want, models[0].Capabilities.ToolCalling)
			}
		})
	}
}

func TestOllamaDiscoveryBoundsConcurrentShowRequestsAndPreservesOrder(t *testing.T) {
	ids := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	started := make(chan struct{}, len(ids))
	release := make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			var tags []ollamaTagsModel
			for _, id := range ids {
				tags = append(tags, ollamaTagsModel{Name: id})
			}
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{Models: tags})
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities": []string{"completion", "tools"},
			"model_info": map[string]any{
				"general.architecture": "gptoss", "gptoss.context_length": 131072,
			},
		})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var models []modelcatalog.Model
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		models, err = NewRosterClient(
			server.Client(),
		).Discover(ctx, modelcatalog.Provider{BaseURL: server.URL})
	}()
	for range 4 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("four model detail requests did not run concurrently")
		}
	}
	select {
	case <-started:
		t.Fatal("more than four model detail requests ran concurrently")
	default:
	}
	unblock()
	<-done
	if err != nil || len(models) != len(ids) {
		t.Fatalf("roster = %+v, %v", models, err)
	}
	for n, model := range models {
		if model.ProviderModel != ids[n] || model.MaxContextTokens != 131072 {
			t.Fatalf("model %d = %s, context %d; want %s, context 131072",
				n, model.ProviderModel, model.MaxContextTokens, ids[n])
		}
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
				Known:       true,
				Chat:        true,
				Text:        true,
				ToolCalling: true,
				ImageInput:  true,
				Reasoning:   true,
				AudioInput:  true,
			}},
		{"unknown-ignored", []string{"completion", "embeddings", "unknown-cap"},
			modelcatalog.Capabilities{Known: true, Chat: true, Text: true}},
		{"reported empty", []string{}, modelcatalog.Capabilities{Known: true, Text: true}},
		{"empty", nil, modelcatalog.Capabilities{Text: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilitiesFromOllamaShow(tc.caps)
			if !reflect.DeepEqual(got, tc.want.WithProvenance("derived")) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
