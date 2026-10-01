// Package config loads, validates, and resolves q15 runtime configuration.
package config

const (
	defaultMemoryRecentTurns    = 6
	defaultScheduleMaxJobs      = 64
	defaultScheduleMaxRunTurns  = 16
	maxScheduleJobs             = 1000
	maxScheduleRunTurns         = 128
	runtimeWorkspaceLocalDir    = "/workspace"
	runtimeMediaLocalDir        = "/media"
	runtimeSkillsLocalDir       = "/skills"
	runtimeStateLocalDir        = "/var/lib/q15/agent"
	runtimeExecutionServiceAddr = "q15-exec:50051"
)

// DefaultBridgeListenTarget is the conventional chat-contract bridge listen
// target once a deployment provisions the bridge socket volume. It is
// documented rather than applied: an empty ListenTarget keeps the listener
// disabled, so a stack can run before that volume exists.
const DefaultBridgeListenTarget = "unix:///run/q15/bridge.sock"

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
//
// The interactive and cognition models are NOT configured here: they are
// runtime state, auto-selected from the live roster on first run and then
// persisted (see internal/selectionstore) so switches survive restart.
type Agent struct {
	Name              string   `yaml:"name"`
	MemoryRecentTurns int      `yaml:"memory_recent_turns"`
	Tools             Tools    `yaml:"tools"`
	Telegram          Telegram `yaml:"telegram"`
	Bridge            Bridge   `yaml:"bridge"`
}

// Tools defines optional agent tool settings.
type Tools struct {
	WebSearch  WebSearchTool  `yaml:"web_search"`
	Embeddings EmbeddingsTool `yaml:"embeddings"`
	Schedule   ScheduleTool   `yaml:"schedule"`
}

// WebSearchTool defines optional web_search settings.
type WebSearchTool struct {
	BraveAPIKeyEnv string `yaml:"brave_api_key_env"`
}

// EmbeddingsTool defines optional embedding source/search tool settings.
//
// Provider selects the embedding backend: "gemini" (the default, so existing
// configurations keep working unchanged) or "openai" for any OpenAI-compatible
// /embeddings endpoint. The api_key_env/base_url_env fields belong to the
// openai provider; base_url_env alone selects a local unauthenticated endpoint.
type EmbeddingsTool struct {
	QdrantURLEnv    string `yaml:"qdrant_url_env"`
	Provider        string `yaml:"provider"`
	GeminiAPIKeyEnv string `yaml:"gemini_api_key_env"`
	APIKeyEnv       string `yaml:"api_key_env"`
	BaseURLEnv      string `yaml:"base_url_env"`
	Model           string `yaml:"model"`
	Dimensions      int    `yaml:"dimensions"`
	BatchSize       int    `yaml:"batch_size"`
}

// ScheduleTool defines execution limits for agent-created scheduled jobs.
type ScheduleTool struct {
	MaxJobs     int `yaml:"max_jobs"`
	MaxRunTurns int `yaml:"max_run_turns"`
}

// Telegram defines Telegram integration settings for an agent.
type Telegram struct {
	Token             string  `yaml:"token"`
	TokenEnv          string  `yaml:"token_env"`
	AllowedUserIDs    []int64 `yaml:"allowed_user_ids"`
	AllowedUserIDsEnv string  `yaml:"allowed_user_ids_env"`
}

// Bridge defines the chat-contract bridge listener settings for an agent.
type Bridge struct {
	// ListenTarget is where the agent serves the frozen chat contract: a
	// unix:///path/to/socket target. TCP is refused here and by the bridge's
	// listener: the chat surface is the identity surface, so it is never
	// published on a host:port. Empty disables the listener: the shared
	// socket volume that backs the conventional DefaultBridgeListenTarget is
	// provisioned by a later slice, so a deployment opts in once that volume
	// exists.
	ListenTarget string `yaml:"listen_target"`
}

// ExecutionRuntime is the resolved q15-exec runtime contract.
type ExecutionRuntime struct {
	ServiceAddress string
}

// ToolsRuntime is the resolved runtime tool configuration for an agent.
type ToolsRuntime struct {
	WebSearch  WebSearchToolRuntime
	Embeddings EmbeddingsToolRuntime
	Schedule   ScheduleToolRuntime
}

// WebSearchToolRuntime is the resolved runtime configuration for web_search.
type WebSearchToolRuntime struct {
	BraveAPIKey string
}

// EmbeddingsToolRuntime is the resolved runtime configuration for embedding tools.
type EmbeddingsToolRuntime struct {
	Enabled      bool
	QdrantURL    string
	Provider     string
	GeminiAPIKey string
	APIKey       string
	BaseURL      string
	Model        string
	Dimensions   int
	BatchSize    int
}

// ScheduleToolRuntime is the resolved scheduled-job policy.
type ScheduleToolRuntime struct {
	MaxJobs     int
	MaxRunTurns int
}

// AgentRuntime is the resolved runtime config for the configured agent. It
// carries runtime bits only; the current model selection is resolved live via
// the modelcatalog.Registry and persisted via internal/selectionstore.
type AgentRuntime struct {
	Name                   string
	WorkspaceLocalDir      string
	MemoryLocalDir         string
	MediaLocalDir          string
	SkillsLocalDir         string
	StateLocalDir          string
	MemoryRecentTurns      int
	Execution              ExecutionRuntime
	Tools                  ToolsRuntime
	TelegramToken          string
	TelegramAllowedUserIDs []int64
	BridgeListenTarget     string
}
