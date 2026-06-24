package modelcatalog

import (
	"context"
	"time"
)

// Source labels identify where a discovered model entry originated.
const (
	SourceOllama    = "ollama"
	SourceOpenAI    = "openai"
	SourceModelsDev = "models-dev"
)

// Capabilities is the normalized capability set discovered for one model.
type Capabilities struct {
	Text        bool
	ImageInput  bool
	AudioInput  bool
	ToolCalling bool
	Reasoning   bool
}

// Model is one discovered model entry with optional cost/performance metadata.
//
// It is the authoritative runtime shape: the registry, the selection adapter,
// and the provider client factory all operate on this type.
//
// Zero-valued numeric/string fields mean "unknown" — the model is still usable,
// it is just unscored for selection purposes.
type Model struct {
	// ProviderName identifies which provider hosts this model.
	ProviderName string
	// ProviderType is the canonical provider type (providertypes.Ollama, etc.).
	ProviderType string
	// ProviderBaseURL is the provider's API base URL.
	ProviderBaseURL string
	// ProviderAPIKey is the resolved API key for this provider.
	ProviderAPIKey string
	// ProviderModel is the provider-side model identifier (e.g.
	// "kimi-k2.7-code:cloud" for an Ollama model including its tag).
	ProviderModel string
	// Ref is the agent-side model reference: the tag-stripped, slash-normalized
	// provider model id (e.g. "kimi-k2.7-code"). It is what the engine, the
	// selection planner, and the agent.model config value use.
	Ref string
	// Name is a human-readable label. When no label is known it equals
	// ProviderModel.
	Name string
	// Capabilities is the discovered or inferred capability set.
	Capabilities Capabilities
	// CostTier is "cheap", "standard", or "expensive". Empty means unknown.
	CostTier string
	// CostPerMTokIn is the input price per million tokens in USD. Zero means
	// unknown, not free.
	CostPerMTokIn float64
	// CostPerMTokOut is the output price per million tokens in USD. Zero means
	// unknown, not free.
	CostPerMTokOut float64
	// MaxContextTokens is the context-window limit. Zero means unknown.
	MaxContextTokens int
	// MaxOutputTokens is the maximum output length. Zero means unknown.
	MaxOutputTokens int
	// BenchmarkScores maps benchmark name (e.g. "human_eval") to score.
	BenchmarkScores map[string]float64
	// Source identifies where the roster entry originated: "ollama", "openai",
	// "models-dev".
	Source string

	// Quality signals for token-plan providers (no per-token cost).
	//
	// ParameterCount is the total model parameter count (from Ollama
	// /api/show model_info.general.parameter_count). Zero means unknown.
	ParameterCount int64
	// ReleaseDate is the model release date (from models.dev). Zero means
	// unknown.
	ReleaseDate time.Time
	// VideoInput reports whether the model accepts video input (from models.dev
	// modalities.input).
	VideoInput bool
	// StructuredOutput reports whether the model supports structured/JSON output
	// (from models.dev top-level structured_output bool).
	StructuredOutput bool
}

// Options controls discovery behavior for one provider.
type Options struct {
	// Enabled selects whether discovery runs for this provider. When false the
	// caller skips this provider entirely (no HTTP).
	Enabled bool
	// ModelsDev selects whether roster entries are enriched from the models.dev
	// catalog.
	ModelsDev bool
	// Include is an optional whitelist of glob patterns (path.Match syntax)
	// applied to ProviderModel. Empty means keep all.
	Include []string
	// Exclude is an optional blacklist of glob patterns applied after Include.
	Exclude []string
}

// Provider is the provider information discovery needs. It is deliberately a
// standalone struct so catalog ports do not depend on config.
type Provider struct {
	Name    string
	Type    string
	BaseURL string
	APIKey  string
	Options Options
}

// Catalog discovers the live model roster for one provider.
type Catalog interface {
	Discover(ctx context.Context, p Provider) ([]Model, error)
}
