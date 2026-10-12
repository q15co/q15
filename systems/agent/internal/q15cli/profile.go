package q15cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
	"go.yaml.in/yaml/v3"
)

const (
	// defaultOllamaBaseURL is the address a freshly rendered stack talks to
	// ollama on. The install profile runs ollama on the same host as the agent,
	// so the endpoint is a plain local port rather than a service name.
	defaultOllamaBaseURL = "http://127.0.0.1:11434"

	// The embedding collection is created at this dimension and a later
	// mismatch is a hard error in the agent, never a silent degradation, so
	// the default is written down once here.
	defaultEmbeddingModel      = "qwen3-embedding-0.6b"
	defaultEmbeddingDimensions = 1024
	defaultEmbeddingBatchSize  = 128
)

// Profile is the deployment-shaped input the CLI needs to render a
// configuration. It carries no secrets: every credential stays the name of an
// environment variable, so rendering never writes one to disk.
type Profile struct {
	// AgentName is the agent's display name.
	AgentName string
	// OllamaBaseURL is the chat provider's endpoint.
	OllamaBaseURL string
	// EmbeddingsURLEnv names the environment variable holding the
	// OpenAI-compatible embeddings endpoint the agent calls at run time.
	EmbeddingsURLEnv string
	// QdrantURLEnv names the environment variable holding the vector store URL.
	QdrantURLEnv string
	// BridgeListenTarget is the chat bridge's unix socket.
	BridgeListenTarget string
}

// DefaultProfile is the profile a new stack renders: one ollama endpoint
// serving both chat and embeddings, the embedding dimension fixed at
// collection-creation time, and the conventional bridge socket. The
// environment variable names are the ones the Compose example already uses, so
// a new install and an existing deployment describe the same environment.
func DefaultProfile() Profile {
	return Profile{
		AgentName:          "Q15",
		OllamaBaseURL:      defaultOllamaBaseURL,
		EmbeddingsURLEnv:   "Q15_EMBEDDINGS_BASE_URL",
		QdrantURLEnv:       "Q15_QDRANT_URL",
		BridgeListenTarget: config.DefaultBridgeListenTarget,
	}
}

// Config renders the profile as the agent's own configuration type and
// validates it, so a configuration the CLI writes is one the agent accepts.
// Drift between the two is impossible by construction rather than caught by a
// test comparing them.
func (p Profile) Config() (config.Config, error) {
	name := strings.TrimSpace(p.AgentName)
	if name == "" {
		return config.Config{}, errors.New("profile: agent name is required")
	}
	baseURL := strings.TrimSpace(p.OllamaBaseURL)
	if baseURL == "" {
		return config.Config{}, errors.New("profile: ollama base URL is required")
	}
	embeddingsURLEnv := strings.TrimSpace(p.EmbeddingsURLEnv)
	if embeddingsURLEnv == "" {
		return config.Config{}, errors.New(
			"profile: embeddings URL environment variable is required",
		)
	}
	qdrantURLEnv := strings.TrimSpace(p.QdrantURLEnv)
	if qdrantURLEnv == "" {
		return config.Config{}, errors.New(
			"profile: vector store URL environment variable is required",
		)
	}

	cfg := config.Config{
		SchemaVersion: config.SchemaVersion,
		Providers: []config.Provider{
			{
				Name:    "ollama",
				Type:    providertypes.Ollama,
				BaseURL: baseURL,
				// A locally served model has no models.dev entry to be
				// enriched with, so discovery stays offline.
				Discovery: config.ProviderDiscovery{ModelsDev: false},
			},
		},
		Agent: &config.Agent{
			Name: name,
			Tools: config.Tools{
				Embeddings: config.EmbeddingsTool{
					QdrantURLEnv: qdrantURLEnv,
					Provider:     embed.ProviderOpenAI,
					BaseURLEnv:   embeddingsURLEnv,
					Model:        defaultEmbeddingModel,
					Dimensions:   defaultEmbeddingDimensions,
					BatchSize:    defaultEmbeddingBatchSize,
				},
			},
			Bridge: config.Bridge{ListenTarget: p.BridgeListenTarget},
		},
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("profile: %w", err)
	}
	return cfg, nil
}

// Marshal renders the validated configuration as the YAML a stack consumes.
func (p Profile) Marshal() ([]byte, error) {
	cfg, err := p.Config()
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(cfg)
}
