// Package config loads, validates, and resolves q15 runtime configuration.
package config

const (
	defaultMemoryRecentTurns    = 6
	runtimeWorkspaceLocalDir    = "/workspace"
	runtimeMediaLocalDir        = "/media"
	runtimeSkillsLocalDir       = "/skills"
	runtimeExecutionServiceAddr = "q15-exec:50051"
)

// Config is the top-level structure loaded from config.yaml.
type Config struct {
	Providers []Provider `yaml:"providers"`
	Agent     *Agent     `yaml:"agent"`
}

// Provider defines a named model provider entry in config.yaml.
type Provider struct {
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type"`
	BaseURL   string            `yaml:"base_url"`
	KeyEnv    string            `yaml:"key_env"`
	Discovery ProviderDiscovery `yaml:"discovery,omitempty"`
}

// ProviderDiscovery configures model-roster discovery for one provider.
// Discovery is mandatory (always on); these options control enrichment and
// filtering only.
type ProviderDiscovery struct {
	// ModelsDev selects whether discovered models are enriched with
	// cost/context/benchmark metadata from the models.dev catalog.
	ModelsDev bool `yaml:"models_dev"`
	// Include is an optional whitelist of glob patterns (path.Match syntax)
	// applied to discovered provider model IDs. Empty means keep all.
	Include []string `yaml:"include"`
	// Exclude is an optional blacklist of glob patterns applied after Include.
	Exclude []string `yaml:"exclude"`
}

// Agent defines one configured q15 agent instance.
type Agent struct {
	Name              string   `yaml:"name"`
	Model             string   `yaml:"model"`
	CognitionModel    string   `yaml:"cognition_model"`
	MemoryRecentTurns int      `yaml:"memory_recent_turns"`
	Tools             Tools    `yaml:"tools"`
	Telegram          Telegram `yaml:"telegram"`
}

// Tools defines optional agent tool settings.
type Tools struct {
	WebSearch  WebSearchTool  `yaml:"web_search"`
	Embeddings EmbeddingsTool `yaml:"embeddings"`
}

// WebSearchTool defines optional web_search settings.
type WebSearchTool struct {
	BraveAPIKeyEnv string `yaml:"brave_api_key_env"`
}

// EmbeddingsTool defines optional embedding source/search tool settings.
type EmbeddingsTool struct {
	QdrantURLEnv    string `yaml:"qdrant_url_env"`
	GeminiAPIKeyEnv string `yaml:"gemini_api_key_env"`
	Model           string `yaml:"model"`
	Dimensions      int    `yaml:"dimensions"`
}

// Telegram defines Telegram integration settings for an agent.
type Telegram struct {
	Token             string  `yaml:"token"`
	TokenEnv          string  `yaml:"token_env"`
	AllowedUserIDs    []int64 `yaml:"allowed_user_ids"`
	AllowedUserIDsEnv string  `yaml:"allowed_user_ids_env"`
}

// ExecutionRuntime is the resolved q15-exec runtime contract.
type ExecutionRuntime struct {
	ServiceAddress string
}

// ToolsRuntime is the resolved runtime tool configuration for an agent.
type ToolsRuntime struct {
	WebSearch  WebSearchToolRuntime
	Embeddings EmbeddingsToolRuntime
}

// WebSearchToolRuntime is the resolved runtime configuration for web_search.
type WebSearchToolRuntime struct {
	BraveAPIKey string
}

// EmbeddingsToolRuntime is the resolved runtime configuration for embedding tools.
type EmbeddingsToolRuntime struct {
	Enabled      bool
	QdrantURL    string
	GeminiAPIKey string
	Model        string
	Dimensions   int
}

// AgentRuntime is the resolved runtime config for the configured agent. It
// carries runtime bits plus the static current-model seed refs; model metadata
// and availability are resolved live via the modelcatalog.Registry at turn
// time.
type AgentRuntime struct {
	Name                     string
	CurrentModelRef          string
	CurrentCognitionModelRef string
	WorkspaceLocalDir        string
	MemoryLocalDir           string
	MediaLocalDir            string
	SkillsLocalDir           string
	MemoryRecentTurns        int
	Execution                ExecutionRuntime
	Tools                    ToolsRuntime
	TelegramToken            string
	TelegramAllowedUserIDs   []int64
}
