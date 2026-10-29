package config

import (
	"fmt"
	"math"
	"net/url"
	"os"

	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// History configures relational conversation context separately from library search.
type History struct {
	EmbeddingAPIKeyEnv        string  `yaml:"embedding_api_key_env"`
	DatabaseURLEnv            string  `yaml:"database_url_env"`
	EmbeddingBaseURL          string  `yaml:"embedding_base_url"`
	EmbeddingProvider         string  `yaml:"embedding_provider"`
	EmbeddingModel            string  `yaml:"embedding_model"`
	EmbeddingDimensions       int     `yaml:"embedding_dimensions"`
	EmbeddingBatchSize        int     `yaml:"embedding_batch_size"`
	EmbeddingBatchTokens      int     `yaml:"embedding_batch_tokens"`
	SummaryProvider           string  `yaml:"summary_provider"`
	SummaryModel              string  `yaml:"summary_model"`
	InitialContextMaxTokens   int     `yaml:"initial_context_max_tokens"`
	InitialContextMaxFraction float64 `yaml:"initial_context_max_fraction"`
	RecentTurns               int     `yaml:"recent_turns"`
	RetrievalLimit            int     `yaml:"retrieval_limit"`
	MinSimilarity             float32 `yaml:"min_similarity"`
}

// HistoryRuntime contains resolved settings; credentials never belong in diagnostics.
type HistoryRuntime struct {
	EmbeddingAPIKey string
	History
	DatabaseURL    string
	SummaryBaseURL string
	SummaryAPIKey  string
}

// ResolveHistory resolves only history dependencies, so offline import needs no transport credentials.
func (c Config) ResolveHistory() (HistoryRuntime, error) {
	if c.Agent == nil {
		return HistoryRuntime{}, fmt.Errorf("agent is required")
	}
	h := c.Agent.History
	if h.DatabaseURLEnv == "" {
		h.DatabaseURLEnv = "Q15_DATABASE_URL"
	}
	if h.EmbeddingProvider == "" {
		h.EmbeddingProvider = "ollama"
	}
	if h.EmbeddingBaseURL == "" {
		h.EmbeddingBaseURL = "http://q15-ollama:11434"
	}
	if h.EmbeddingModel == "" {
		h.EmbeddingModel = "embeddinggemma-2:740m"
	}
	if h.EmbeddingDimensions == 0 {
		h.EmbeddingDimensions = 768
	}
	if h.EmbeddingBatchSize == 0 {
		h.EmbeddingBatchSize = 8
	}
	if h.EmbeddingBatchTokens == 0 {
		h.EmbeddingBatchTokens = 8192
	}
	if h.SummaryModel == "" {
		h.SummaryModel = "minimax-m3.1-flash"
	}
	if h.InitialContextMaxTokens == 0 {
		h.InitialContextMaxTokens = 100000
	}
	if h.InitialContextMaxFraction == 0 {
		h.InitialContextMaxFraction = 0.25
	}
	if h.RecentTurns == 0 {
		h.RecentTurns = 4
	}
	if h.RetrievalLimit == 0 {
		h.RetrievalLimit = 6
	}
	if h.MinSimilarity == 0 {
		h.MinSimilarity = 0.35
	}
	if err := h.validate(); err != nil {
		return HistoryRuntime{}, err
	}
	result := HistoryRuntime{History: h}
	dsn, _, err := lookupSecretEnvValue(h.DatabaseURLEnv)
	if err != nil {
		return result, err
	}
	if dsn == "" && os.Getenv("PGHOST") != "" {
		dsn, err = ResolveDatabaseURL(h.DatabaseURLEnv)
		if err != nil {
			return result, err
		}
	}
	result.DatabaseURL = dsn
	if h.EmbeddingAPIKeyEnv != "" {
		key, err := resolveSecretEnvValue(h.EmbeddingAPIKeyEnv)
		if err != nil {
			return result, err
		}
		result.EmbeddingAPIKey = key
	}

	for _, provider := range c.Providers {
		if providertypes.NormalizeOrEmpty(provider.Type) != providertypes.Ollama {
			continue
		}
		base, err := url.Parse(provider.BaseURL)
		if err != nil {
			continue
		}
		if h.SummaryProvider != "" {
			if provider.Name != h.SummaryProvider {
				continue
			}
		} else if base.Scheme != "https" || base.Host != "ollama.com" {
			continue
		}
		if result.SummaryBaseURL != "" {
			return result, fmt.Errorf("history.summary_provider must select one Ollama provider")
		}
		result.SummaryProvider = provider.Name
		result.SummaryBaseURL = provider.BaseURL
		key, err := ResolveProviderAPIKey(provider, providertypes.Ollama)
		if err != nil {
			return result, err
		}
		result.SummaryAPIKey = key
	}
	return result, nil
}

func (h History) validate() error {
	if h.EmbeddingProvider != "" && h.EmbeddingProvider != "ollama" &&
		h.EmbeddingProvider != "openai" {
		return fmt.Errorf("agent.history.embedding_provider must be ollama or openai")
	}
	if h.EmbeddingDimensions < 0 || h.EmbeddingDimensions > 2000 || h.EmbeddingBatchSize < 0 ||
		h.EmbeddingBatchSize > 128 ||
		h.EmbeddingBatchTokens < 0 || h.EmbeddingBatchTokens > 65536 ||
		h.InitialContextMaxTokens < 0 ||
		(h.InitialContextMaxTokens > 0 && h.InitialContextMaxTokens < 1024) ||
		h.RecentTurns < 0 ||
		h.RecentTurns > 32 ||
		h.RetrievalLimit < 0 ||
		h.RetrievalLimit > 32 ||
		math.IsNaN(
			float64(h.MinSimilarity),
		) || math.IsInf(float64(h.MinSimilarity), 0) || h.MinSimilarity < 0 ||
		h.MinSimilarity > 1 {
		return fmt.Errorf("agent.history contains invalid dimensions, context limits or similarity")
	}
	if fraction := h.InitialContextMaxFraction; math.IsNaN(fraction) || math.IsInf(fraction, 0) ||
		fraction < 0 ||
		fraction > 1 {
		return fmt.Errorf(
			"agent.history.initial_context_max_fraction must be greater than zero and at most one",
		)
	}
	return nil
}
