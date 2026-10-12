package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// TestDefaultProfileLoadsThroughTheAgentLoader is the point of folding the CLI
// into the agent module: the configuration it renders is read back by the
// agent's own loader, so drift between the two is impossible by construction
// rather than caught by comparing a golden file.
func TestDefaultProfileLoadsThroughTheAgentLoader(t *testing.T) {
	rendered, err := DefaultProfile().Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(rendered), "schema_version: 1") {
		t.Fatalf("rendered config = %q, want the schema version written", rendered)
	}
	// Secrets stay environment variable names: rendering never writes a value.
	if !strings.Contains(string(rendered), "base_url_env: Q15_EMBEDDINGS_BASE_URL") {
		t.Fatalf("rendered config = %q, want the embeddings endpoint by name", rendered)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, rendered, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if loaded.SchemaVersion != config.SchemaVersion {
		t.Fatalf("loaded schema version = %d, want %d", loaded.SchemaVersion, config.SchemaVersion)
	}
	if loaded.Agent == nil {
		t.Fatal("loaded agent = nil, want the rendered agent")
	}
	if loaded.Agent.Name != "Q15" {
		t.Fatalf("loaded agent name = %q, want %q", loaded.Agent.Name, "Q15")
	}

	provider, ok := loaded.FindProvider("ollama")
	if !ok {
		t.Fatal("loaded providers do not contain ollama")
	}
	if provider.Type != providertypes.Ollama {
		t.Fatalf("provider type = %q, want %q", provider.Type, providertypes.Ollama)
	}
	if provider.BaseURL != defaultOllamaBaseURL {
		t.Fatalf("provider base url = %q, want %q", provider.BaseURL, defaultOllamaBaseURL)
	}
	if provider.KeyEnv != "" {
		t.Fatalf("provider key env = %q, want none for a local ollama", provider.KeyEnv)
	}

	embeddings := loaded.Agent.Tools.Embeddings
	if embeddings.Provider != embed.ProviderOpenAI {
		t.Fatalf("embeddings provider = %q, want %q", embeddings.Provider, embed.ProviderOpenAI)
	}
	if embeddings.Model != defaultEmbeddingModel {
		t.Fatalf("embeddings model = %q, want %q", embeddings.Model, defaultEmbeddingModel)
	}
	if embeddings.Dimensions != defaultEmbeddingDimensions {
		t.Fatalf(
			"embeddings dimensions = %d, want %d",
			embeddings.Dimensions,
			defaultEmbeddingDimensions,
		)
	}

	if loaded.Agent.Bridge.ListenTarget != config.DefaultBridgeListenTarget {
		t.Fatalf(
			"bridge listen target = %q, want %q",
			loaded.Agent.Bridge.ListenTarget,
			config.DefaultBridgeListenTarget,
		)
	}
}

func TestProfileRejectsIncompleteInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{name: "agent name", mutate: func(p *Profile) { p.AgentName = "  " }},
		{name: "ollama base url", mutate: func(p *Profile) { p.OllamaBaseURL = "" }},
		{name: "embeddings env", mutate: func(p *Profile) { p.EmbeddingsURLEnv = " " }},
		{name: "vector store env", mutate: func(p *Profile) { p.QdrantURLEnv = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := DefaultProfile()
			test.mutate(&profile)

			if _, err := profile.Config(); err == nil {
				t.Fatal("Config() error = nil, want a rejection")
			}
		})
	}
}
