package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewCatalog_OllamaEndToEndWithEnrichment(t *testing.T) {
	newCacheDir(t)

	// models.dev server with a matching ollama-cloud entry (real schema).
	modelsDevPayload := writeModelsDevPayload(t, modelsDevFile{
		"ollama-cloud": {
			Name: "Ollama Cloud",
			Models: map[string]modelsDevModel{
				"kimi-k2": {
					Name:       "Kimi K2",
					Limit:      modelsDevLimit{Context: 256000, Output: 8192},
					Modalities: modelsDevModalities{Input: []string{"text", "image"}},
					Cost:       modelsDevCost{Input: 0.5, Output: 1.5},
					ToolCall:   true,
					Reasoning:  true,
				},
				"nemotron": {
					Name:     "Nemotron",
					Limit:    modelsDevLimit{Context: 128000},
					Cost:     modelsDevCost{Input: 0.3, Output: 0.6},
					ToolCall: true,
				},
			},
		},
	})

	var modelsDevHits int
	modelsDevServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			modelsDevHits++
			_, _ = w.Write(modelsDevPayload)
		}),
	)
	defer modelsDevServer.Close()

	// Ollama server: /api/tags returns two models, /api/show returns caps.
	ollamaMux := http.NewServeMux()
	ollamaMux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "kimi-k2:cloud"},
				{"name": "nemotron:cloud"},
				{"name": "embed-model:latest"},
			},
		})
	})
	ollamaMux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities": []string{"completion", "tools"},
		})
	})
	ollamaServer := httptest.NewServer(ollamaMux)
	defer ollamaServer.Close()

	// Override the default models.dev URL so NewCatalog's client hits our test
	// server.
	t.Run("enrichment+filters", func(t *testing.T) {
		old := defaultCatalogModelsDevURL
		defaultCatalogModelsDevURL = modelsDevServer.URL
		t.Cleanup(func() { defaultCatalogModelsDevURL = old })

		catalog := NewCatalog(ollamaServer.Client())
		models, err := catalog.Discover(context.Background(), Provider{
			Type:    "ollama",
			Name:    "ollama-cloud",
			BaseURL: ollamaServer.URL, // mock roster endpoint
			APIKey:  "test-key",
			Options: Options{
				ModelsDev:         true,
				ModelsDevProvider: "ollama-cloud",
				Include:           []string{"kimi-*", "nemotron:*"},
				Exclude:           []string{"*:latest"},
			},
		})
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		// embed-model excluded by Include filter; kimi and nemotron kept.
		if len(models) != 2 {
			t.Fatalf("expected 2 models after filters, got %d", len(models))
		}
		if modelsDevHits == 0 {
			t.Fatal("models.dev was never fetched")
		}

		byProviderModel := map[string]Model{}
		for _, m := range models {
			byProviderModel[m.ProviderModel] = m
		}
		kimi := byProviderModel["kimi-k2:cloud"]
		if kimi.MaxContextTokens != 256000 {
			t.Errorf("kimi MaxContextTokens = %d, want 256000", kimi.MaxContextTokens)
		}
		if !kimi.Capabilities.ImageInput {
			t.Error("kimi should have ImageInput from models.dev modalities")
		}
		if !kimi.Capabilities.ToolCalling {
			t.Error("kimi should have ToolCalling from models.dev top-level tool_call")
		}
		if !kimi.Capabilities.Reasoning {
			t.Error("kimi should have Reasoning from models.dev top-level reasoning")
		}
		if kimi.CostPerMTokIn != 0.5 {
			t.Errorf(
				"kimi CostPerMTokIn = %f, want 0.5 (USD per MTok, no conversion)",
				kimi.CostPerMTokIn,
			)
		}
	})
}

func TestNewCatalog_OpenAICompatibleWithoutEnrichment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "moonshot-v1-128k"}, {"id": "moonshot-v1-32k"}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	catalog := NewCatalog(server.Client())
	models, err := catalog.Discover(context.Background(), Provider{
		Type:    "openai-compatible",
		BaseURL: server.URL + "/v1",
		APIKey:  "secret",
		Options: Options{},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	for _, m := range models {
		if !m.Capabilities.Text {
			t.Error("expected Text capability")
		}
	}
}

func TestNewCatalog_CodexReturnsEmpty(t *testing.T) {
	catalog := NewCatalog(http.DefaultClient)
	models, err := catalog.Discover(context.Background(), Provider{
		Type:    "openai-codex",
		Options: Options{},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models from codex, got %d", len(models))
	}
}

func TestNewCatalog_UnsupportedProviderType(t *testing.T) {
	catalog := NewCatalog(http.DefaultClient)
	_, err := catalog.Discover(context.Background(), Provider{
		Type:    "imaginary",
		Options: Options{},
	})
	if err == nil {
		t.Fatal("expected error for unsupported provider type")
	}
}

func TestNoopCatalog(t *testing.T) {
	catalog := NoopCatalog{}
	models, err := catalog.Discover(context.Background(), Provider{})
	if err != nil {
		t.Fatalf("NoopCatalog.Discover error: %v", err)
	}
	if models != nil {
		t.Fatalf("expected nil models, got %d", len(models))
	}
}

func TestNewCatalog_NilHTTPClient(t *testing.T) {
	// NewCatalog(nil) must not panic and must produce a working Catalog.
	catalog := NewCatalog(nil)
	if catalog == nil {
		t.Fatal("NewCatalog(nil) returned nil")
	}
	// Verify it's usable for codex (no HTTP needed).
	models, err := catalog.Discover(context.Background(), Provider{Type: "openai-codex"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if models != nil {
		t.Fatalf("expected nil for codex, got %d models", len(models))
	}
}

func TestNewCatalog_DeepInfraChatRosterWithEnrichment(t *testing.T) {
	newCacheDir(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/openai/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
            {"id":"openai/gpt-oss-20b","metadata":{"tags":["chat","reasoning"]}},
			{"id":"Qwen/Qwen3-235B-A22B-Instruct-2507","metadata":{"tags":["chat"]}},
            {"id":"uncatalogued/chat","metadata":{"tags":["chat"]}},
            {"id":"BAAI/bge-m3","metadata":{"tags":["embed"]}}
        ]}`))
	})
	mux.HandleFunc("/api.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(modelsDevFile{
			"deepinfra": {
				Doc: "https://deepinfra.com/models",
				Models: map[string]modelsDevModel{
					"Qwen/Qwen3-235B-A22B-Instruct-2507": {ToolCall: true},
					"openai/gpt-oss-20b": {
						ToolCall: true, StructuredOutput: true,
						Limit: modelsDevLimit{Context: 131072, Output: 16384},
						Cost:  modelsDevCost{Input: 0.03, Output: 0.14},
					},
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	old := defaultCatalogModelsDevURL
	defaultCatalogModelsDevURL = server.URL + "/api.json"
	t.Cleanup(func() { defaultCatalogModelsDevURL = old })
	catalog := NewCatalog(server.Client())
	models, err := catalog.Discover(context.Background(), Provider{
		Type: "openai-compatible", BaseURL: server.URL + "/v1/openai",
		Options: Options{ModelsDev: true, ModelsDevProvider: "deepinfra"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("roster = %+v, want only the three chat models", models)
	}
	known := models[0]
	if !known.Capabilities.Known || !known.Capabilities.ToolCalling ||
		!known.StructuredOutputKnown ||
		!known.StructuredOutput {
		t.Errorf("catalogued model lost capability metadata: %+v", known)
	}
	if known.MaxContextTokens != 131072 || known.MaxOutputTokens != 16384 ||
		known.CostPerMTokIn != 0.03 {
		t.Errorf("catalogued model lost limits or cost: %+v", known)
	}
	if !models[1].Capabilities.Known || !models[1].Capabilities.ToolCalling ||
		models[1].ProviderModel != "Qwen/Qwen3-235B-A22B-Instruct-2507" {
		t.Errorf("mixed-case model lost metadata or its provider ID: %+v", models[1])
	}
	if models[2].Capabilities.Known || models[2].StructuredOutputKnown {
		t.Errorf("unmatched model claims known metadata: %+v", models[2])
	}
}
