// Package config loads, validates, and resolves q15 runtime configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	defaultMemoryRecentTurns    = 6
	runtimeWorkspaceLocalDir    = "/workspace"
	runtimeSkillsLocalDir       = "/skills"
	runtimeExecutionServiceAddr = "q15-exec:50051"
)

// Config is the top-level structure loaded from config.yaml.
type Config struct {
	Providers []Provider `yaml:"providers"`
	Models    []Model    `yaml:"models"`
	Agent     *Agent     `yaml:"agent"`
}

// Provider defines a named model provider entry in config.yaml.
type Provider struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	KeyEnv  string `yaml:"key_env"`
}

// Model defines a named model entry in config.yaml.
type Model struct {
	Name          string   `yaml:"name"`
	Provider      string   `yaml:"provider"`
	ProviderModel string   `yaml:"provider_model"`
	Capabilities  []string `yaml:"capabilities"`
}

// Agent defines one configured q15 agent instance.
type Agent struct {
	Name              string   `yaml:"name"`
	Models            []string `yaml:"models"`
	MemoryRecentTurns int      `yaml:"memory_recent_turns"`
	Telegram          Telegram `yaml:"telegram"`
}

// Telegram defines Telegram integration settings for an agent.
type Telegram struct {
	Token             string  `yaml:"token"`
	TokenEnv          string  `yaml:"token_env"`
	AllowedUserIDs    []int64 `yaml:"allowed_user_ids"`
	AllowedUserIDsEnv string  `yaml:"allowed_user_ids_env"`
}

// ModelCapabilities is the normalized capability set for one configured model.
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

// ExecutionRuntime is the resolved q15-exec runtime contract.
type ExecutionRuntime struct {
	ServiceAddress string
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

var defaultModelCapabilities = ModelCapabilities{
	Text: true,
}

// Load reads config.yaml from path and validates it.
func Load(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, errors.New("config path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := ensureSingleDocument(dec); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

func ensureSingleDocument(dec *yaml.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("multiple YAML documents are not supported")
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

	return resolveSecretEnvValue(envName)
}

// TelegramAllowedUserIDs resolves the Telegram allow-list from inline values or
// allowed_user_ids_env. The environment source accepts comma-separated or
// whitespace-separated integer user IDs and also supports the standard
// *_FILE companion via resolveSecretEnvValue.
func (a Agent) TelegramAllowedUserIDs() ([]int64, error) {
	envName := strings.TrimSpace(a.Telegram.AllowedUserIDsEnv)
	if len(a.Telegram.AllowedUserIDs) > 0 && envName != "" {
		return nil, errors.New(
			"set either telegram.allowed_user_ids or telegram.allowed_user_ids_env, not both",
		)
	}
	if envName == "" {
		return normalizeAllowedUserIDs(a.Telegram.AllowedUserIDs)
	}

	value, err := resolveSecretEnvValue(envName)
	if err != nil {
		return nil, err
	}
	return parseAllowedUserIDs(value)
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
			return nil, fmt.Errorf("%s model %q is not defined in models", fieldPath, modelRef)
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
			apiKey, err = resolveSecretEnvValue(provider.KeyEnv)
			if err != nil {
				return nil, fmt.Errorf("provider %q requires %w", provider.Name, err)
			}
		case "openai-codex":
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

		resolvedModels = append(resolvedModels, AgentModelRuntime{
			Ref:             modelRef,
			ProviderName:    provider.Name,
			ProviderType:    providerType,
			ProviderBaseURL: strings.TrimSpace(provider.BaseURL),
			ProviderAPIKey:  apiKey,
			ProviderModel:   modelCfg.resolvedProviderModel(),
			Capabilities:    capabilities,
		})
	}

	token, err := agentCfg.TelegramToken()
	if err != nil {
		return nil, fmt.Errorf("resolve telegram token for agent %q: %w", agentCfg.Name, err)
	}
	allowedUserIDs, err := agentCfg.TelegramAllowedUserIDs()
	if err != nil {
		return nil, fmt.Errorf(
			"resolve telegram allowed users for agent %q: %w",
			agentCfg.Name,
			err,
		)
	}

	memoryRecentTurns := agentCfg.MemoryRecentTurns
	if memoryRecentTurns == 0 {
		memoryRecentTurns = defaultMemoryRecentTurns
	}

	return &AgentRuntime{
		Name:                   strings.TrimSpace(agentCfg.Name),
		Models:                 resolvedModels,
		WorkspaceLocalDir:      runtimeWorkspaceLocalDir,
		MemoryLocalDir:         "/memory",
		SkillsLocalDir:         runtimeSkillsLocalDir,
		MemoryRecentTurns:      memoryRecentTurns,
		Execution:              ExecutionRuntime{ServiceAddress: runtimeExecutionServiceAddr},
		TelegramToken:          token,
		TelegramAllowedUserIDs: allowedUserIDs,
	}, nil
}

func (c Config) validate() error {
	if c.Agent != nil && len(c.Providers) == 0 {
		return errors.New("providers cannot be empty when agent is configured")
	}

	providers := make(map[string]struct{}, len(c.Providers))
	for i, provider := range c.Providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if _, ok := providers[name]; ok {
			return fmt.Errorf("duplicate provider name %q", name)
		}
		providers[name] = struct{}{}

		if strings.TrimSpace(provider.Type) == "" {
			return fmt.Errorf("providers[%d].type is required", i)
		}
		normalizedType := normalizeProviderType(provider.Type)
		if normalizedType == "" {
			return fmt.Errorf("providers[%d].type %q is not supported", i, provider.Type)
		}
		if normalizedType == "openai-compatible" && strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf(
				"providers[%d].base_url is required for openai-compatible providers",
				i,
			)
		}
		if normalizedType == "openai-compatible" && strings.TrimSpace(provider.KeyEnv) == "" {
			return fmt.Errorf("providers[%d].key_env is required", i)
		}
	}

	models := make(map[string]struct{}, len(c.Models))
	for i, model := range c.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			return fmt.Errorf("models[%d].name is required", i)
		}
		if strings.Contains(name, "/") {
			return fmt.Errorf("models[%d].name must not contain /", i)
		}
		if _, ok := models[name]; ok {
			return fmt.Errorf("duplicate model name %q", name)
		}
		models[name] = struct{}{}

		providerName := strings.TrimSpace(model.Provider)
		if providerName == "" {
			return fmt.Errorf("models[%d].provider is required", i)
		}
		if _, ok := providers[providerName]; !ok {
			return fmt.Errorf("models[%d].provider %q is not defined in providers", i, providerName)
		}
		if _, err := normalizeModelCapabilities(model.Capabilities); err != nil {
			return fmt.Errorf("models[%d].capabilities: %w", i, err)
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
		return errors.New("models cannot be empty when agent is configured")
	}
	for j, modelRef := range modelRefs {
		if _, ok := models[modelRef]; !ok {
			return fmt.Errorf("agent.models[%d] model %q is not defined in models", j, modelRef)
		}
	}
	if strings.TrimSpace(c.Agent.Telegram.Token) == "" &&
		strings.TrimSpace(c.Agent.Telegram.TokenEnv) == "" {
		return errors.New("agent.telegram requires token or token_env")
	}
	allowedUserIDsEnv := strings.TrimSpace(c.Agent.Telegram.AllowedUserIDsEnv)
	if len(c.Agent.Telegram.AllowedUserIDs) == 0 && allowedUserIDsEnv == "" {
		return errors.New("agent.telegram requires allowed_user_ids or allowed_user_ids_env")
	}
	if len(c.Agent.Telegram.AllowedUserIDs) > 0 && allowedUserIDsEnv != "" {
		return errors.New(
			"agent.telegram.allowed_user_ids and agent.telegram.allowed_user_ids_env are mutually exclusive",
		)
	}
	if len(c.Agent.Telegram.AllowedUserIDs) > 0 {
		if _, err := normalizeAllowedUserIDs(c.Agent.Telegram.AllowedUserIDs); err != nil {
			return fmt.Errorf("agent.telegram.allowed_user_ids: %w", err)
		}
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
				"[%d] uses unsupported %q format; define models and reference their name",
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

func parseAllowedUserIDs(value string) ([]int64, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(fields) == 0 {
		return nil, errors.New("must contain at least one user id")
	}

	ids := make([]int64, 0, len(fields))
	for i, field := range fields {
		id, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("[%d] %q is not a valid int64", i, field)
		}
		ids = append(ids, id)
	}

	return normalizeAllowedUserIDs(ids)
}
