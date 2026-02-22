package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Providers []Provider `mapstructure:"provider"`
	Agents    []Agent    `mapstructure:"agent"`
}

type Provider struct {
	Name    string `mapstructure:"name"`
	Type    string `mapstructure:"type"`
	BaseURL string `mapstructure:"base_url"`
	KeyEnv  string `mapstructure:"key_env"`
}

type Agent struct {
	Name     string   `mapstructure:"name"`
	Model    string   `mapstructure:"model"`
	Models   []string `mapstructure:"models"` // legacy, ignored for q15.toml shape
	Sandbox  Sandbox  `mapstructure:"sandbox"`
	Telegram Telegram `mapstructure:"telegram"`
}

type Sandbox struct {
	ContainerName    string `mapstructure:"container_name"`
	FromImage        string `mapstructure:"from_image"`
	WorkspaceHostDir string `mapstructure:"workspace_host_dir"`
	WorkspaceDir     string `mapstructure:"workspace_dir"`
	Network          string `mapstructure:"network"`
}

type Telegram struct {
	Token          string  `mapstructure:"token"`
	TokenEnv       string  `mapstructure:"token_env"`
	AllowedUserIDs []int64 `mapstructure:"allowed_user_ids"`
}

// AgentRuntime is the resolved runtime config for one agent after env vars and
// provider/model references have been processed.
type AgentRuntime struct {
	Name                   string
	ProviderType           string
	ProviderBaseURL        string
	ProviderAPIKey         string
	Models                 []string
	SandboxContainerName   string
	SandboxFromImage       string
	WorkspaceHostDir       string
	WorkspaceDir           string
	SandboxNetwork         string
	TelegramToken          string
	TelegramAllowedUserIDs []int64
}

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
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

func LoadAgentRuntimes(path string) ([]AgentRuntime, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return cfg.ResolveAgentRuntimes()
}

func (c Config) FindAgent(name string) (Agent, bool) {
	for _, agent := range c.Agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return Agent{}, false
}

func (c Config) FindProvider(name string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.Name == name {
			return provider, true
		}
	}
	return Provider{}, false
}

func (c Config) Validate() error {
	return c.validate()
}

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

func (c Config) ResolveAgentRuntimes() ([]AgentRuntime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	runtimes := make([]AgentRuntime, 0, len(c.Agents))
	for i, agentCfg := range c.Agents {
		providerName, modelName, err := parseModelRef(agentCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("agents[%d].model: %w", i, err)
		}

		provider, ok := c.FindProvider(providerName)
		if !ok {
			return nil, fmt.Errorf(
				"agents[%d].model provider %q is not defined in providers",
				i,
				providerName,
			)
		}

		providerType := normalizeProviderType(provider.Type)
		if providerType == "" {
			return nil, fmt.Errorf(
				"agents[%d].model provider %q has unsupported type %q",
				i,
				provider.Name,
				provider.Type,
			)
		}

		apiKey := strings.TrimSpace(os.Getenv(strings.TrimSpace(provider.KeyEnv)))
		if apiKey == "" {
			return nil, fmt.Errorf(
				"provider %q requires env var %q",
				provider.Name,
				provider.KeyEnv,
			)
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
		sandboxNetwork, err := normalizeSandboxNetwork(agentCfg.Sandbox.Network)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox network for agent %q: %w", agentCfg.Name, err)
		}

		runtimes = append(runtimes, AgentRuntime{
			Name:                   strings.TrimSpace(agentCfg.Name),
			ProviderType:           providerType,
			ProviderBaseURL:        strings.TrimSpace(provider.BaseURL),
			ProviderAPIKey:         apiKey,
			Models:                 []string{modelName},
			SandboxContainerName:   strings.TrimSpace(agentCfg.Sandbox.ContainerName),
			SandboxFromImage:       strings.TrimSpace(agentCfg.Sandbox.FromImage),
			WorkspaceHostDir:       strings.TrimSpace(agentCfg.Sandbox.WorkspaceHostDir),
			WorkspaceDir:           strings.TrimSpace(agentCfg.Sandbox.WorkspaceDir),
			SandboxNetwork:         sandboxNetwork,
			TelegramToken:          token,
			TelegramAllowedUserIDs: allowedUserIDs,
		})
	}

	return runtimes, nil
}

func (c Config) validate() error {
	if len(c.Providers) == 0 {
		return errors.New("provider cannot be empty")
	}
	if len(c.Agents) == 0 {
		return errors.New("agent cannot be empty")
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
		if strings.TrimSpace(provider.KeyEnv) == "" {
			return fmt.Errorf("provider[%d].key_env is required", i)
		}
	}

	agents := make(map[string]struct{}, len(c.Agents))
	for i, agent := range c.Agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			return fmt.Errorf("agent[%d].name is required", i)
		}
		if _, ok := agents[name]; ok {
			return fmt.Errorf("duplicate agent name %q", name)
		}
		agents[name] = struct{}{}

		if len(agent.Models) > 0 {
			return fmt.Errorf(
				"agent[%d].models is not supported in q15.toml; use agent[%d].model = \"provider/model\"",
				i,
				i,
			)
		}

		if _, _, err := parseModelRef(agent.Model); err != nil {
			return fmt.Errorf("agent[%d].model: %w", i, err)
		}
		if strings.TrimSpace(agent.Sandbox.ContainerName) == "" {
			return fmt.Errorf("agent[%d].sandbox.container_name is required", i)
		}
		if strings.TrimSpace(agent.Sandbox.FromImage) == "" {
			return fmt.Errorf("agent[%d].sandbox.from_image is required", i)
		}
		if strings.TrimSpace(agent.Sandbox.WorkspaceHostDir) == "" {
			return fmt.Errorf("agent[%d].sandbox.workspace_host_dir is required", i)
		}
		if strings.TrimSpace(agent.Sandbox.WorkspaceDir) == "" {
			return fmt.Errorf("agent[%d].sandbox.workspace_dir is required", i)
		}
		if _, err := normalizeSandboxNetwork(agent.Sandbox.Network); err != nil {
			return fmt.Errorf("agent[%d].sandbox.network: %w", i, err)
		}
		if strings.TrimSpace(agent.Telegram.Token) == "" &&
			strings.TrimSpace(agent.Telegram.TokenEnv) == "" {
			return fmt.Errorf("agent[%d].telegram requires token or token_env", i)
		}
		if _, err := normalizeAllowedUserIDs(agent.Telegram.AllowedUserIDs); err != nil {
			return fmt.Errorf("agent[%d].telegram.allowed_user_ids: %w", i, err)
		}
	}

	return nil
}

func parseModelRef(modelRef string) (providerName string, modelName string, err error) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return "", "", errors.New("is required")
	}

	providerName, modelName, ok := strings.Cut(modelRef, "/")
	if !ok {
		return "", "", errors.New(`must be in "provider/model" format`)
	}
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)
	if providerName == "" || modelName == "" {
		return "", "", errors.New(`must be in "provider/model" format`)
	}

	return providerName, modelName, nil
}

func normalizeProviderType(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case "moonshot", "openai-compatible":
		return "openai-compatible"
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

func normalizeSandboxNetwork(network string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "disabled":
		return "disabled", nil
	case "enabled":
		return "enabled", nil
	default:
		return "", errors.New(`must be "enabled" or "disabled"`)
	}
}
