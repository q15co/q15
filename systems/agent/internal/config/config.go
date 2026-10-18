// Package config loads, validates, and resolves q15 runtime configuration.
package config

const (
	defaultMemoryRecentTurns = 6
	// defaultMemoryWorkingBudgetBytes and defaultMemoryOpenLoopsBudgetBytes
	// are the memory byte budgets whose absent keys resolve to these values.
	defaultMemoryWorkingBudgetBytes   = 8192
	defaultMemoryOpenLoopsBudgetBytes = 4096
	defaultScheduleMaxJobs            = 64
	defaultScheduleMaxRunTurns        = 16
	maxScheduleJobs                   = 1000
	maxScheduleRunTurns               = 128
	runtimeWorkspaceLocalDir          = "/workspace"
	runtimeMediaLocalDir              = "/media"
	runtimeSkillsLocalDir             = "/skills"
	runtimeStateLocalDir              = "/var/lib/q15/agent"
	runtimeExecutionServiceAddr       = "q15-exec:50051"
)

// DefaultBridgeListenTarget is the conventional chat-contract bridge listen
// target once a deployment provisions the bridge socket volume. It is
// documented rather than applied: an empty ListenTarget keeps the listener
// disabled, so a stack can run before that volume exists.
const DefaultBridgeListenTarget = "unix:///run/q15/bridge.sock"

// DefaultMemoryWorkingBudgetBytes and DefaultMemoryOpenLoopsBudgetBytes are
// the byte budgets a configuration resolves to when the fields are absent.
// Read-only status reporting judges sizes against these when it cannot read a
// rendered agent configuration, so the fallback spells the same values the
// loader's resolution applies.
const (
	DefaultMemoryWorkingBudgetBytes   = defaultMemoryWorkingBudgetBytes
	DefaultMemoryOpenLoopsBudgetBytes = defaultMemoryOpenLoopsBudgetBytes
)

// SchemaVersion is the configuration schema version this build reads and
// writes. The CLI writes it into every configuration it renders, and loading
// refuses a configuration written by a build that understands a different
// shape; see checkSchemaVersion.
const SchemaVersion = 1

// Config is the top-level structure loaded from config.yaml.
type Config struct {
	// SchemaVersion is the configuration schema version. It is optional on
	// read: a configuration written before the field existed is a version-1
	// configuration, so an absent value stays legal. See
	// validateSchemaVersion for what is refused, and why.
	SchemaVersion int `yaml:"schema_version,omitempty"`

	Primary   *ModelBinding  `yaml:"primary,omitempty"`
	Fallbacks []ModelBinding `yaml:"fallbacks,omitempty"`
	Decision  *ModelBinding  `yaml:"decision,omitempty"`
	Embed     *ModelBinding  `yaml:"embed,omitempty"`
	Providers []Provider     `yaml:"providers"`
	Agent     *Agent         `yaml:"agent"`
}

// Provider defines a named model provider entry in config.yaml.
type Provider struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	KeyEnv  string `yaml:"key_env"`
	// CapabilityOverrides correct capabilities per native model ID.
	CapabilityOverrides map[string]map[string]bool `yaml:"capability_overrides,omitempty"`
	Discovery           ProviderDiscovery          `yaml:"discovery,omitempty"`
}

// ProviderDiscovery configures model-roster discovery for one provider.
// Discovery is mandatory (always on); these options control enrichment and
// filtering only.
type ProviderDiscovery struct {
	// ModelsDev selects whether discovered models are enriched with
	// cost/context/benchmark metadata from the models.dev catalog.
	ModelsDev bool `yaml:"models_dev"`
	// ModelsDevProvider overrides automatic catalog matching for proxies or
	// endpoints whose provider identity is ambiguous.
	ModelsDevProvider string `yaml:"models_dev_provider,omitempty"`
	// Include is an optional whitelist of glob patterns (path.Match syntax)
	// applied to discovered provider model IDs. Empty means keep all.
	Include []string `yaml:"include"`
	// Exclude is an optional blacklist of glob patterns applied after Include.
	Exclude []string `yaml:"exclude"`
}

// Agent defines one configured q15 agent instance.
//
// Model role bindings belong to the top-level Config. Explicit interactive
// switches and per-job cognition overrides remain persisted runtime state
// (see internal/selectionstore).
type Agent struct {
	Name              string `yaml:"name"`
	MemoryRecentTurns int    `yaml:"memory_recent_turns"`
	// MemoryWorkingBudgetBytes is the byte budget whose overrun fires the
	// conversion pass's working_memory_overbudget rule; absent selects the
	// documented default.
	MemoryWorkingBudgetBytes int `yaml:"memory_working_budget_bytes"`
	// MemoryOpenLoopsBudgetBytes is the open-loops byte budget the
	// working-memory consolidation job's open_loops_overbudget rule enforces;
	// absent selects the documented default.
	MemoryOpenLoopsBudgetBytes int      `yaml:"memory_open_loops_budget_bytes"`
	Tools                      Tools    `yaml:"tools"`
	Telegram                   Telegram `yaml:"telegram"`
	Bridge                     Bridge   `yaml:"bridge"`
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
// The vector store is addressed separately: qdrant_url_env names its endpoint
// and qdrant_api_key_env the credential a hosted one authenticates with, which
// is empty for a local one.
type EmbeddingsTool struct {
	QdrantURLEnv string `yaml:"qdrant_url_env"`
	// QdrantAPIKeyEnv names the credential a hosted vector store authenticates
	// with. Empty for a local one, which takes no key.
	QdrantAPIKeyEnv string `yaml:"qdrant_api_key_env"`
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
	QdrantAPIKey string
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
	Name              string
	WorkspaceLocalDir string
	MemoryLocalDir    string
	MediaLocalDir     string
	SkillsLocalDir    string
	StateLocalDir     string
	MemoryRecentTurns int
	// MemoryWorkingBudgetBytes is the working-memory byte budget the semantic
	// conversion registration's overbudget state rule enforces.
	MemoryWorkingBudgetBytes int
	// MemoryOpenLoopsBudgetBytes is the open-loops byte budget the
	// working-memory consolidation job's open_loops_overbudget rule enforces.
	MemoryOpenLoopsBudgetBytes int
	Execution                  ExecutionRuntime
	Tools                      ToolsRuntime
	TelegramToken              string
	TelegramAllowedUserIDs     []int64
	BridgeListenTarget         string
}
