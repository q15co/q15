package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/discovery"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestTEIDiscoverySatisfiesEmbeddingRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"chat"}]}`))
		case "/tei/info":
			_, _ = w.Write(
				[]byte(
					`{"model_id":"Qwen/Qwen3-Embedding-0.6B","served_model_name":"qwen3-embedding-0.6b","model_type":{"embedding":{"pooling":"last_token"}}}`,
				),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := modelcatalog.New([]modelcatalog.Provider{
		{Name: "chat", Type: "openai-compatible", BaseURL: server.URL + "/chat"},
		{Name: "tei", Type: "openai-compatible", BaseURL: server.URL + "/tei"},
	}, discovery.NewCatalog(server.Client()), time.Hour, time.Second)
	registry.Refresh(context.Background())
	cfg := Config{
		Providers: []Provider{
			{Name: "chat", Type: "openai-compatible", BaseURL: server.URL + "/chat"},
			{Name: "tei", Type: "openai-compatible", BaseURL: server.URL + "/tei"},
		},
		Primary: &ModelBinding{Provider: "chat", Model: "chat"},
		Embed:   &ModelBinding{Provider: "tei", Model: "qwen3-embedding-0.6b"},
		Agent: &Agent{
			Name: "q15",
			Tools: Tools{Embeddings: EmbeddingsTool{
				Provider: "openai", Model: "qwen3-embedding-0.6b", Dimensions: 1024,
				QdrantURLEnv: "Q15_QDRANT_URL", BaseURLEnv: "Q15_EMBEDDINGS_BASE_URL",
			}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TEI config validation: %v", err)
	}
	key, err := ResolveProviderAPIKey(cfg.Providers[1], "openai-compatible")
	if err != nil || key != "" {
		t.Fatalf("TEI key resolution = %q, %v", key, err)
	}
	bindings, err := cfg.ValidateRoles(registry)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("TEI role validation = %+v, %v", bindings, err)
	}
	cfg.Primary = cfg.Embed
	if _, err := cfg.ValidateRoles(registry); err == nil ||
		!strings.Contains(err.Error(), "role primary") ||
		!strings.Contains(err.Error(), "missing capability chat") {
		t.Fatalf("TEI accepted as chat primary: %v", err)
	}
}

type roleCatalog []modelcatalog.Model

func (c roleCatalog) Discover(
	context.Context,
	modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return c, nil
}
func roleRegistry(caps modelcatalog.Capabilities) *modelcatalog.Registry {
	r := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "api"}},
		roleCatalog{{ProviderModel: "model", Capabilities: caps}},
		time.Hour,
		time.Second,
	)
	r.Refresh(context.Background())
	return r
}
func TestRoleValidationRejectsMissingCapability(t *testing.T) {
	for _, role := range []string{"primary", "fallbacks[0]", "decision", "embed"} {
		t.Run(role, func(t *testing.T) {
			b := &ModelBinding{Provider: "api", Model: "model"}
			cfg := Config{}
			switch role {
			case "primary":
				cfg.Primary = b
			case "fallbacks[0]":
				cfg.Fallbacks = []ModelBinding{*b}
			case "decision":
				cfg.Decision = b
			case "embed":
				cfg.Embed = b
			}
			_, err := cfg.ValidateRoles(roleRegistry(modelcatalog.Capabilities{}))
			required := "chat"
			if role == "decision" {
				required = "decision"
			}
			if role == "embed" {
				required = "embed"
			}
			if err == nil || !strings.Contains(err.Error(), role) ||
				!strings.Contains(err.Error(), "model") ||
				!strings.Contains(err.Error(), required) {
				t.Fatalf("role validation = %v", err)
			}
		})
	}
}
func TestDualBindAbortsWithoutOptIn(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		b := ModelBinding{Provider: "api", Model: "model"}
		decider := b
		cfg := Config{Decision: &decider}
		if fallback {
			cfg.Fallbacks = []ModelBinding{b}
		} else {
			cfg.Primary = &b
		}
		reg := roleRegistry(modelcatalog.Capabilities{Chat: true, Decision: true})
		if _, err := cfg.ValidateRoles(reg); err == nil {
			t.Fatal("dual bind accepted without reason")
		}
		cfg.Decision.AllowDualBind = "single model"
		if _, err := cfg.ValidateRoles(reg); err == nil {
			t.Fatal("dual bind accepted without partitions")
		}
		cfg.Decision.Limits = RoleLimits{RequestsPerMinute: 1, RequestBytesPerMinute: 1000}
		if fallback {
			cfg.Fallbacks[0].Limits = cfg.Decision.Limits
		} else {
			cfg.Primary.Limits = cfg.Decision.Limits
		}
		if _, err := cfg.ValidateRoles(reg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredAgentRequiresPrimaryAndEmbedBindings(t *testing.T) {
	cfg := Config{Agent: &Agent{Name: "q15"}}
	registry := roleRegistry(modelcatalog.Capabilities{Chat: true, Embed: true})
	if _, err := cfg.ValidateRoles(registry); err == nil ||
		!strings.Contains(err.Error(), "role primary") {
		t.Fatalf("unbound primary = %v", err)
	}
	cfg.Primary = &ModelBinding{Provider: "api", Model: "model"}
	cfg.Agent.Tools.Embeddings.Model = "model"
	if _, err := cfg.ValidateRoles(registry); err == nil ||
		!strings.Contains(err.Error(), "role embed") {
		t.Fatalf("unbound embed = %v", err)
	}
	cfg.Embed = &ModelBinding{Provider: "api", Model: "model"}
	if _, err := cfg.ValidateRoles(registry); err != nil {
		t.Fatal(err)
	}
}
