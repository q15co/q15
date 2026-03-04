// Package config loads, validates, and resolves q15 runtime configuration.
package config

import (
	"errors"
	"fmt"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

// Config is the top-level structure loaded from config.toml.
type Config struct {
	Providers []Provider `mapstructure:"provider"`
	Agents    []Agent    `mapstructure:"agent"`
}

// Provider defines a named model provider entry in config.toml.
type Provider struct {
	Name    string `mapstructure:"name"`
	Type    string `mapstructure:"type"`
	BaseURL string `mapstructure:"base_url"`
	KeyEnv  string `mapstructure:"key_env"`
}

// Agent defines one configured q15 agent instance.
type Agent struct {
	Name              string   `mapstructure:"name"`
	Models            []string `mapstructure:"models"` // ordered fallback list of provider/model refs
	MemoryRecentTurns int      `mapstructure:"memory_recent_turns"`
	Sandbox           Sandbox  `mapstructure:"sandbox"`
	Telegram          Telegram `mapstructure:"telegram"`
}

// Sandbox defines container and workspace settings for agent execution.
type Sandbox struct {
	ContainerName    string        `mapstructure:"container_name"`
	WorkspaceHostDir string        `mapstructure:"workspace_host_dir"`
	WorkspaceDir     string        `mapstructure:"workspace_dir"`
	Proxy            *SandboxProxy `mapstructure:"proxy"`
}

// SandboxProxy defines optional egress-proxy settings for sandbox traffic.
type SandboxProxy struct {
	ContainerProxyHost string             `mapstructure:"container_proxy_host"` // optional override
	Secrets            []string           `mapstructure:"secrets"`              // alias list, env name derived (e.g. gh_token -> GH_TOKEN)
	Rules              []SandboxProxyRule `mapstructure:"rule"`
}

// SandboxProxyRule defines host/path matches and request mutations.
type SandboxProxyRule struct {
	Name               string                               `mapstructure:"name"`
	MatchHosts         []string                             `mapstructure:"match_hosts"`
	MatchPathPrefixes  []string                             `mapstructure:"match_path_prefixes"`
	SetHeader          map[string]string                    `mapstructure:"set_header"`
	ReplacePlaceholder []SandboxProxyPlaceholderReplacement `mapstructure:"replace_placeholder"`
}

// SandboxProxyPlaceholderReplacement replaces placeholders with secret values.
type SandboxProxyPlaceholderReplacement struct {
	Placeholder string   `mapstructure:"placeholder"`
	Secret      string   `mapstructure:"secret"`
	In          []string `mapstructure:"in"` // allowed v1: header, query, path
}

// Telegram defines Telegram integration settings for an agent.
type Telegram struct {
	Token          string  `mapstructure:"token"`
	TokenEnv       string  `mapstructure:"token_env"`
	AllowedUserIDs []int64 `mapstructure:"allowed_user_ids"`
}

// AgentModelRuntime is the resolved runtime config for one provider/model ref.
type AgentModelRuntime struct {
	Ref             string
	ProviderName    string
	ProviderType    string
	ProviderBaseURL string
	ProviderAPIKey  string
	ModelName       string
}

// SandboxProxyRuntime is the resolved runtime form of SandboxProxy.
type SandboxProxyRuntime struct {
	Enabled              bool
	ListenAddr           string
	ContainerProxyHost   string
	CACertContainerPath  string
	NoProxy              []string
	SetLowercaseProxyEnv bool
	SecretValues         map[string]string // alias -> resolved secret value
	Rules                []SandboxProxyRule
}

const (
	defaultSandboxProxyListenAddr          = "0.0.0.0:0"
	defaultSandboxProxyCACertContainerPath = "/run/q15-proxy/ca.crt"
	defaultMemoryDir                       = "/memory"
	defaultMemoryRecentTurns               = 6
)

var (
	proxySecretAliasRE         = regexp.MustCompile(`^[a-z0-9_-]+$`)
	defaultSandboxProxyNoProxy = []string{"localhost", "127.0.0.1", "::1"}
)

// AgentRuntime is the resolved runtime config for one configured agent.
type AgentRuntime struct {
	Name                   string
	Models                 []AgentModelRuntime
	SandboxContainerName   string
	WorkspaceHostDir       string
	WorkspaceDir           string
	MemoryHostDir          string
	MemoryDir              string
	MemoryRecentTurns      int
	SandboxProxy           *SandboxProxyRuntime
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
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// LoadAgentRuntimes reads and resolves agent runtime settings from path.
func LoadAgentRuntimes(path string) ([]AgentRuntime, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("config path is required")
	}

	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return cfg.resolveAgentRuntimes("")
}

// FindAgent returns the configured agent by name.
func (c Config) FindAgent(name string) (Agent, bool) {
	for _, agent := range c.Agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return Agent{}, false
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

// ResolveAgentRuntimes resolves all configured agents into runtime values.
func (c Config) ResolveAgentRuntimes() ([]AgentRuntime, error) {
	return c.resolveAgentRuntimes("")
}

func (c Config) resolveAgentRuntimes(_ string) ([]AgentRuntime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	runtimes := make([]AgentRuntime, 0, len(c.Agents))
	for i, agentCfg := range c.Agents {
		modelRefs, err := agentCfg.modelRefs()
		if err != nil {
			return nil, fmt.Errorf("agents[%d].models: %w", i, err)
		}

		resolvedModels := make([]AgentModelRuntime, 0, len(modelRefs))
		for j, modelRef := range modelRefs {
			providerName, modelName, err := parseModelRef(modelRef)
			if err != nil {
				return nil, fmt.Errorf("agents[%d].models[%d]: %w", i, j, err)
			}

			fieldPath := fmt.Sprintf("agents[%d].models[%d]", i, j)

			provider, ok := c.FindProvider(providerName)
			if !ok {
				return nil, fmt.Errorf(
					"%s provider %q is not defined in providers",
					fieldPath,
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

			resolvedModels = append(resolvedModels, AgentModelRuntime{
				Ref:             modelRef,
				ProviderName:    provider.Name,
				ProviderType:    providerType,
				ProviderBaseURL: strings.TrimSpace(provider.BaseURL),
				ProviderAPIKey:  apiKey,
				ModelName:       modelName,
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
		sandboxProxy, err := resolveSandboxProxyRuntime(agentCfg.Sandbox.Proxy)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox proxy for agent %q: %w", agentCfg.Name, err)
		}
		memoryRecentTurns := agentCfg.MemoryRecentTurns
		if memoryRecentTurns == 0 {
			memoryRecentTurns = defaultMemoryRecentTurns
		}
		workspaceHostDir := strings.TrimSpace(agentCfg.Sandbox.WorkspaceHostDir)

		runtimes = append(runtimes, AgentRuntime{
			Name:                   strings.TrimSpace(agentCfg.Name),
			Models:                 resolvedModels,
			SandboxContainerName:   strings.TrimSpace(agentCfg.Sandbox.ContainerName),
			WorkspaceHostDir:       workspaceHostDir,
			WorkspaceDir:           strings.TrimSpace(agentCfg.Sandbox.WorkspaceDir),
			MemoryHostDir:          filepath.Join(workspaceHostDir, ".q15-memory"),
			MemoryDir:              defaultMemoryDir,
			MemoryRecentTurns:      memoryRecentTurns,
			SandboxProxy:           sandboxProxy,
			TelegramToken:          token,
			TelegramAllowedUserIDs: allowedUserIDs,
		})
	}

	return runtimes, nil
}

func (c Config) validate() error {
	if len(c.Agents) == 0 {
		return nil
	}
	if len(c.Providers) == 0 {
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

		modelRefs, err := agent.modelRefs()
		if err != nil {
			return fmt.Errorf("agent[%d].models: %w", i, err)
		}
		for j, modelRef := range modelRefs {
			if _, _, err := parseModelRef(modelRef); err != nil {
				return fmt.Errorf("agent[%d].models[%d]: %w", i, j, err)
			}
		}
		if strings.TrimSpace(agent.Sandbox.ContainerName) == "" {
			return fmt.Errorf("agent[%d].sandbox.container_name is required", i)
		}
		if strings.TrimSpace(agent.Sandbox.WorkspaceHostDir) == "" {
			return fmt.Errorf("agent[%d].sandbox.workspace_host_dir is required", i)
		}
		if strings.TrimSpace(agent.Sandbox.WorkspaceDir) == "" {
			return fmt.Errorf("agent[%d].sandbox.workspace_dir is required", i)
		}
		if err := validateSandboxProxy(agent.Sandbox.Proxy); err != nil {
			return fmt.Errorf("agent[%d].sandbox.proxy: %w", i, err)
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
		refs = append(refs, modelRef)
	}
	return refs, nil
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

func validateSandboxProxy(proxy *SandboxProxy) error {
	if proxy == nil {
		return nil
	}

	secretAliases, err := normalizeProxySecretAliases(proxy.Secrets)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	if len(secretAliases) == 0 {
		return errors.New("secrets must contain at least one alias")
	}
	secretEnvByAlias := make(map[string]string, len(secretAliases))
	for _, alias := range secretAliases {
		secretEnvByAlias[alias] = proxySecretEnvName(alias)
	}

	for i, rule := range proxy.Rules {
		if len(rule.MatchHosts) == 0 {
			return fmt.Errorf("rule[%d].match_hosts must contain at least one host", i)
		}
		for j, host := range rule.MatchHosts {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("rule[%d].match_hosts[%d] must not be empty", i, j)
			}
		}
		for j, pfx := range rule.MatchPathPrefixes {
			pfx = strings.TrimSpace(pfx)
			if pfx == "" {
				return fmt.Errorf("rule[%d].match_path_prefixes[%d] must not be empty", i, j)
			}
			if !strings.HasPrefix(pfx, "/") {
				return fmt.Errorf("rule[%d].match_path_prefixes[%d] must start with /", i, j)
			}
		}
		for headerName := range rule.SetHeader {
			if strings.TrimSpace(headerName) == "" {
				return fmt.Errorf("rule[%d].set_header contains an empty header name", i)
			}
		}
		for j, repl := range rule.ReplacePlaceholder {
			if strings.TrimSpace(repl.Placeholder) == "" {
				return fmt.Errorf("rule[%d].replace_placeholder[%d].placeholder is required", i, j)
			}
			secretAlias, err := normalizeProxySecretAlias(repl.Secret)
			if err != nil {
				return fmt.Errorf("rule[%d].replace_placeholder[%d].secret: %w", i, j, err)
			}
			if _, ok := secretEnvByAlias[secretAlias]; !ok {
				return fmt.Errorf(
					"rule[%d].replace_placeholder[%d].secret %q is not defined in proxy.secrets",
					i,
					j,
					secretAlias,
				)
			}
			if len(repl.In) == 0 {
				return fmt.Errorf("rule[%d].replace_placeholder[%d].in must not be empty", i, j)
			}
			for k, where := range repl.In {
				where = strings.ToLower(strings.TrimSpace(where))
				switch where {
				case "header", "query", "path":
					// supported in v1
				case "body":
					return fmt.Errorf(
						"rule[%d].replace_placeholder[%d].in[%d]=body is not supported in v1",
						i,
						j,
						k,
					)
				default:
					return fmt.Errorf(
						"rule[%d].replace_placeholder[%d].in[%d] must be header, query, or path",
						i,
						j,
						k,
					)
				}
			}
		}
	}

	return nil
}

func resolveSandboxProxyRuntime(
	proxy *SandboxProxy,
) (*SandboxProxyRuntime, error) {
	if proxy == nil {
		return nil, nil
	}
	if err := validateSandboxProxy(proxy); err != nil {
		return nil, err
	}

	secretAliases, err := normalizeProxySecretAliases(proxy.Secrets)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	secretValues := make(map[string]string, len(secretAliases))
	for _, alias := range secretAliases {
		envName := proxySecretEnvName(alias)
		value := strings.TrimSpace(os.Getenv(envName))
		if value == "" {
			return nil, fmt.Errorf(
				`proxy secret %q requires env var %q`,
				alias,
				envName,
			)
		}
		secretValues[alias] = value
	}

	return &SandboxProxyRuntime{
		Enabled:              true,
		ListenAddr:           defaultSandboxProxyListenAddr,
		ContainerProxyHost:   normalizeSandboxProxyContainerHost(proxy.ContainerProxyHost),
		CACertContainerPath:  defaultSandboxProxyCACertContainerPath,
		NoProxy:              append([]string(nil), defaultSandboxProxyNoProxy...),
		SetLowercaseProxyEnv: true,
		SecretValues:         secretValues,
		Rules:                normalizeSandboxProxyRules(proxy.Rules),
	}, nil
}

func normalizeSandboxProxyRules(rules []SandboxProxyRule) []SandboxProxyRule {
	if len(rules) == 0 {
		return nil
	}

	out := make([]SandboxProxyRule, 0, len(rules))
	for _, rule := range rules {
		normalizedRule := SandboxProxyRule{
			Name:              strings.TrimSpace(rule.Name),
			MatchHosts:        normalizeStringList(rule.MatchHosts, true),
			MatchPathPrefixes: normalizeStringList(rule.MatchPathPrefixes, false),
		}
		if len(rule.SetHeader) > 0 {
			normalizedRule.SetHeader = make(map[string]string, len(rule.SetHeader))
			for k, v := range rule.SetHeader {
				headerName := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(k))
				normalizedRule.SetHeader[headerName] = strings.TrimSpace(v)
			}
		}
		if len(rule.ReplacePlaceholder) > 0 {
			normalizedRule.ReplacePlaceholder = make(
				[]SandboxProxyPlaceholderReplacement,
				0,
				len(rule.ReplacePlaceholder),
			)
			for _, repl := range rule.ReplacePlaceholder {
				normalizedRule.ReplacePlaceholder = append(
					normalizedRule.ReplacePlaceholder,
					SandboxProxyPlaceholderReplacement{
						Placeholder: strings.TrimSpace(repl.Placeholder),
						Secret:      strings.ToLower(strings.TrimSpace(repl.Secret)),
						In:          normalizeStringList(repl.In, true),
					},
				)
			}
		}
		out = append(out, normalizedRule)
	}
	return out
}

func normalizeStringList(values []string, lower bool) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeProxySecretAliases(aliases []string) ([]string, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for i, alias := range aliases {
		normalized, err := normalizeProxySecretAlias(alias)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeProxySecretAlias(alias string) (string, error) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return "", errors.New("alias must not be empty")
	}
	if !proxySecretAliasRE.MatchString(alias) {
		return "", errors.New("alias must contain only a-z, 0-9, _, or -")
	}
	return alias, nil
}

func proxySecretEnvName(alias string) string {
	alias = strings.ReplaceAll(alias, "-", "_")
	return strings.ToUpper(alias)
}

func normalizeSandboxProxyContainerHost(raw string) string {
	if host := strings.TrimSpace(raw); host != "" {
		return host
	}
	if host := strings.TrimSpace(os.Getenv("Q15_SANDBOX_PROXY_CONTAINER_HOST")); host != "" {
		return host
	}
	return "host.containers.internal"
}
