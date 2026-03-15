// Package config loads, validates, and resolves q15 runtime configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config is the top-level structure loaded from config.toml.
type Config struct {
	Providers []Provider `mapstructure:"provider"`
	Models    []Model    `mapstructure:"model"`
	Agent     *Agent     `mapstructure:"agent"`
	Skills    Skills     `mapstructure:"skills"`
}

// Provider defines a named model provider entry in config.toml.
type Provider struct {
	Name    string `mapstructure:"name"`
	Type    string `mapstructure:"type"`
	BaseURL string `mapstructure:"base_url"`
	KeyEnv  string `mapstructure:"key_env"`
}

// Model defines a named model entry in config.toml.
type Model struct {
	Name          string   `mapstructure:"name"`
	Provider      string   `mapstructure:"provider"`
	ProviderModel string   `mapstructure:"provider_model"`
	Capabilities  []string `mapstructure:"capabilities"`
}

// Agent defines one configured q15 agent instance.
type Agent struct {
	Name              string     `mapstructure:"name"`
	Models            []string   `mapstructure:"models"` // ordered fallback list of configured model names
	MemoryRecentTurns int        `mapstructure:"memory_recent_turns"`
	Workspace         Workspace  `mapstructure:"workspace"`
	Execution         *Execution `mapstructure:"execution"`
	Telegram          Telegram   `mapstructure:"telegram"`
}

// Workspace defines the agent-local workspace mount.
type Workspace struct {
	LocalDir string `mapstructure:"local_dir"`
}

// Execution defines execution-service settings for session-backed commands.
type Execution struct {
	ServiceAddress string `mapstructure:"service_address"`
}

// Skills defines the optional shared local directory for agent-authored skills.
type Skills struct {
	LocalDir string `mapstructure:"local_dir"`
}

// Telegram defines Telegram integration settings for an agent.
type Telegram struct {
	Token          string  `mapstructure:"token"`
	TokenEnv       string  `mapstructure:"token_env"`
	AllowedUserIDs []int64 `mapstructure:"allowed_user_ids"`
}

// ModelCapabilities is the normalized capability set for one configured model.
// In v1, ToolCalling is applied directly by routing code; the other fields are
// carried as validated runtime metadata for later fallback and multimodal work.
type ModelCapabilities struct {
	Text        bool
	ImageInput  bool
	ToolCalling bool
	Reasoning   bool
}

// AgentModelRuntime is the resolved runtime config for one configured model.
type AgentModelRuntime struct {
	Ref             string
	ProviderName    string
	ProviderType    string
	ProviderBaseURL string
	ProviderAPIKey  string
	ProviderModel   string
	Capabilities    ModelCapabilities
}

// ExecutionRuntime is the resolved runtime form of Execution.
type ExecutionRuntime struct {
	ServiceAddress string
}

const (
	defaultMemoryRecentTurns = 6
)

// Default to text-only so custom providers do not implicitly opt into tools or
// richer request handling they may not support.
var defaultModelCapabilities = ModelCapabilities{
	Text: true,
}

// AgentRuntime is the resolved runtime config for the configured agent.
type AgentRuntime struct {
	Name                   string
	Models                 []AgentModelRuntime
	WorkspaceLocalDir      string
	MemoryLocalDir         string
	SkillsLocalDir         string
	MemoryRecentTurns      int
	Execution              ExecutionRuntime
	TelegramToken          string
	TelegramAllowedUserIDs []int64
}

// Load reads config.toml from path and validates it.
func Load(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, errors.New("config path is required")
	}

	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// LoadAgentRuntime reads and resolves the configured agent runtime from path.
func LoadAgentRuntime(path string) (*AgentRuntime, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("config path is required")
	}

	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return cfg.ResolveAgentRuntime()
}

// FindProvider returns the configured provider by name.
func (c Config) FindProvider(name string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.Name == name {
			return provider, true
		}
	}
	return Provider{}, false
}

// FindModel returns the configured model by name.
func (c Config) FindModel(name string) (Model, bool) {
	for _, model := range c.Models {
		if model.Name == name {
			return model, true
		}
	}
	return Model{}, false
}

// Validate checks that the config is internally consistent.
func (c Config) Validate() error {
	return c.validate()
}

// TelegramToken resolves the Telegram token from inline value or token_env.
func (a Agent) TelegramToken() (string, error) {
	token := strings.TrimSpace(a.Telegram.Token)
	if token != "" {
		return token, nil
	}

	envName := strings.TrimSpace(a.Telegram.TokenEnv)
	if envName == "" {
		return "", errors.New(
			"telegram token is required (set telegram.token or telegram.token_env)",
		)
	}

	envValue := strings.TrimSpace(os.Getenv(envName))
	if envValue == "" {
		return "", fmt.Errorf("telegram token env var %q is empty", envName)
	}

	return envValue, nil
}

// ResolveAgentRuntime resolves the configured agent into a runtime value.
func (c Config) ResolveAgentRuntime() (*AgentRuntime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	if c.Agent == nil {
		return nil, nil
	}

	agentCfg := c.Agent
	modelRefs, err := agentCfg.modelRefs()
	if err != nil {
		return nil, fmt.Errorf("agent.models: %w", err)
	}

	resolvedModels := make([]AgentModelRuntime, 0, len(modelRefs))
	for j, modelRef := range modelRefs {
		fieldPath := fmt.Sprintf("agent.models[%d]", j)
		modelCfg, ok := c.FindModel(modelRef)
		if !ok {
			return nil, fmt.Errorf(
				"%s model %q is not defined in models",
				fieldPath,
				modelRef,
			)
		}

		providerName := strings.TrimSpace(modelCfg.Provider)
		provider, ok := c.FindProvider(providerName)
		if !ok {
			return nil, fmt.Errorf(
				"%s model %q references undefined provider %q",
				fieldPath,
				modelRef,
				providerName,
			)
		}

		providerType := normalizeProviderType(provider.Type)
		if providerType == "" {
			return nil, fmt.Errorf(
				"%s provider %q has unsupported type %q",
				fieldPath,
				provider.Name,
				provider.Type,
			)
		}

		var apiKey string
		switch providerType {
		case "openai-compatible":
			apiKey = strings.TrimSpace(os.Getenv(strings.TrimSpace(provider.KeyEnv)))
			if apiKey == "" {
				return nil, fmt.Errorf(
					"provider %q requires env var %q",
					provider.Name,
					provider.KeyEnv,
				)
			}
		case "openai-codex":
			// Credentials come from q15 auth store for OpenAI subscription login.
			apiKey = ""
		default:
			return nil, fmt.Errorf(
				"%s provider %q has unsupported type %q",
				fieldPath,
				provider.Name,
				provider.Type,
			)
		}

		capabilities, err := normalizeModelCapabilities(modelCfg.Capabilities)
		if err != nil {
			return nil, fmt.Errorf("%s capabilities: %w", fieldPath, err)
		}

		providerModel := modelCfg.resolvedProviderModel()
		resolvedModels = append(resolvedModels, AgentModelRuntime{
			Ref:             modelRef,
			ProviderName:    provider.Name,
			ProviderType:    providerType,
			ProviderBaseURL: strings.TrimSpace(provider.BaseURL),
			ProviderAPIKey:  apiKey,
			ProviderModel:   providerModel,
			Capabilities:    capabilities,
		})
	}

	token, err := agentCfg.TelegramToken()
	if err != nil {
		return nil, fmt.Errorf("resolve telegram token for agent %q: %w", agentCfg.Name, err)
	}
	allowedUserIDs, err := normalizeAllowedUserIDs(agentCfg.Telegram.AllowedUserIDs)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve telegram allowed users for agent %q: %w",
			agentCfg.Name,
			err,
		)
	}
	executionRuntime, err := resolveExecutionRuntime(agentCfg.Execution)
	if err != nil {
		return nil, fmt.Errorf("resolve execution config for agent %q: %w", agentCfg.Name, err)
	}
	memoryRecentTurns := agentCfg.MemoryRecentTurns
	if memoryRecentTurns == 0 {
		memoryRecentTurns = defaultMemoryRecentTurns
	}
	workspaceLocalDir := strings.TrimSpace(agentCfg.Workspace.LocalDir)
	skillsLocalDir := strings.TrimSpace(c.Skills.LocalDir)

	return &AgentRuntime{
		Name:                   strings.TrimSpace(agentCfg.Name),
		Models:                 resolvedModels,
		WorkspaceLocalDir:      workspaceLocalDir,
		MemoryLocalDir:         filepath.Join(workspaceLocalDir, ".q15-memory"),
		SkillsLocalDir:         skillsLocalDir,
		MemoryRecentTurns:      memoryRecentTurns,
		Execution:              *executionRuntime,
		TelegramToken:          token,
		TelegramAllowedUserIDs: allowedUserIDs,
	}, nil
}

func (c Config) validate() error {
	if err := validateSkills(c.Skills); err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	if c.Agent != nil && len(c.Providers) == 0 {
		return errors.New("provider cannot be empty when agent is configured")
	}

	providers := make(map[string]struct{}, len(c.Providers))
	for i, provider := range c.Providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			return fmt.Errorf("provider[%d].name is required", i)
		}
		if _, ok := providers[name]; ok {
			return fmt.Errorf("duplicate provider name %q", name)
		}
		providers[name] = struct{}{}

		if strings.TrimSpace(provider.Type) == "" {
			return fmt.Errorf("provider[%d].type is required", i)
		}
		normalizedType := normalizeProviderType(provider.Type)
		if normalizedType == "openai-compatible" &&
			strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf(
				"provider[%d].base_url is required for openai-compatible providers",
				i,
			)
		}
		if normalizedType == "openai-compatible" &&
			strings.TrimSpace(provider.KeyEnv) == "" {
			return fmt.Errorf("provider[%d].key_env is required", i)
		}
	}

	models := make(map[string]struct{}, len(c.Models))
	for i, model := range c.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			return fmt.Errorf("model[%d].name is required", i)
		}
		if strings.Contains(name, "/") {
			return fmt.Errorf("model[%d].name must not contain /", i)
		}
		if _, ok := models[name]; ok {
			return fmt.Errorf("duplicate model name %q", name)
		}
		models[name] = struct{}{}

		providerName := strings.TrimSpace(model.Provider)
		if providerName == "" {
			return fmt.Errorf("model[%d].provider is required", i)
		}
		if _, ok := providers[providerName]; !ok {
			return fmt.Errorf(
				"model[%d].provider %q is not defined in providers",
				i,
				providerName,
			)
		}
		if _, err := normalizeModelCapabilities(model.Capabilities); err != nil {
			return fmt.Errorf("model[%d].capabilities: %w", i, err)
		}
	}

	if c.Agent == nil {
		return nil
	}

	name := strings.TrimSpace(c.Agent.Name)
	if name == "" {
		return errors.New("agent.name is required")
	}

	modelRefs, err := c.Agent.modelRefs()
	if err != nil {
		return fmt.Errorf("agent.models: %w", err)
	}
	if len(c.Models) == 0 {
		return errors.New("model cannot be empty when agent is configured")
	}
	for j, modelRef := range modelRefs {
		if _, ok := models[modelRef]; !ok {
			return fmt.Errorf(
				"agent.models[%d] model %q is not defined in models",
				j,
				modelRef,
			)
		}
	}
	if strings.TrimSpace(c.Agent.Workspace.LocalDir) == "" {
		return errors.New("agent.workspace.local_dir is required")
	}
	if !filepath.IsAbs(strings.TrimSpace(c.Agent.Workspace.LocalDir)) {
		return errors.New("agent.workspace.local_dir must be an absolute path")
	}
	if err := validateExecution(c.Agent.Execution); err != nil {
		return fmt.Errorf("agent.execution: %w", err)
	}
	if strings.TrimSpace(c.Agent.Telegram.Token) == "" &&
		strings.TrimSpace(c.Agent.Telegram.TokenEnv) == "" {
		return errors.New("agent.telegram requires token or token_env")
	}
	if _, err := normalizeAllowedUserIDs(c.Agent.Telegram.AllowedUserIDs); err != nil {
		return fmt.Errorf("agent.telegram.allowed_user_ids: %w", err)
	}

	return nil
}

func validateSkills(skills Skills) error {
	localDir := strings.TrimSpace(skills.LocalDir)
	if localDir == "" {
		return nil
	}
	if !filepath.IsAbs(localDir) {
		return errors.New("local_dir must be an absolute path")
	}
	return nil
}

func (a Agent) modelRefs() ([]string, error) {
	if len(a.Models) == 0 {
		return nil, errors.New("must contain at least one model")
	}

	refs := make([]string, 0, len(a.Models))
	for i, modelRef := range a.Models {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef == "" {
			return nil, fmt.Errorf("[%d] must not be empty", i)
		}
		if strings.Contains(modelRef, "/") {
			return nil, fmt.Errorf(
				"[%d] uses unsupported %q format; define [[model]] and reference its name",
				i,
				"provider/model",
			)
		}
		refs = append(refs, modelRef)
	}
	return refs, nil
}

func (m Model) resolvedProviderModel() string {
	providerModel := strings.TrimSpace(m.ProviderModel)
	if providerModel != "" {
		return providerModel
	}
	return strings.TrimSpace(m.Name)
}

func normalizeModelCapabilities(names []string) (ModelCapabilities, error) {
	if len(names) == 0 {
		return defaultModelCapabilities, nil
	}

	var capabilities ModelCapabilities
	for i, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "text":
			capabilities.Text = true
		case "image_input":
			capabilities.ImageInput = true
		case "tool_calling":
			capabilities.ToolCalling = true
		case "reasoning":
			capabilities.Reasoning = true
		default:
			return ModelCapabilities{}, fmt.Errorf("[%d] %q is not supported", i, name)
		}
	}

	return capabilities, nil
}

func normalizeProviderType(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "moonshot", "openai-compatible", "openai_compatible":
		return "openai-compatible"
	case "openai-codex", "openai_codex":
		return "openai-codex"
	default:
		return ""
	}
}

func normalizeAllowedUserIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, errors.New("must contain at least one user id")
	}

	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("[%d] must be greater than 0", i)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("[%d] duplicates user id %d", i, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	return out, nil
}

func validateExecution(execution *Execution) error {
	if execution == nil {
		return errors.New("is required")
	}
	if strings.TrimSpace(execution.ServiceAddress) == "" {
		return errors.New("service_address is required")
	}
	return nil
}

func resolveExecutionRuntime(execution *Execution) (*ExecutionRuntime, error) {
	if err := validateExecution(execution); err != nil {
		return nil, err
	}
	return &ExecutionRuntime{
		ServiceAddress: strings.TrimSpace(execution.ServiceAddress),
	}, nil
}
