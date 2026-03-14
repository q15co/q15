// Package config loads, validates, and resolves q15 proxy-service configuration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/viper"
)

// Config is the top-level structure loaded from the proxy service config file.
type Config struct {
	Service Service `mapstructure:"service"`
	Proxy   Proxy   `mapstructure:"proxy"`
}

// Service defines process-owned listen and state settings.
type Service struct {
	AdminListen       string `mapstructure:"admin_listen"`
	ProxyListen       string `mapstructure:"proxy_listen"`
	AdvertiseProxyURL string `mapstructure:"advertise_proxy_url"`
	StateDir          string `mapstructure:"state_dir"`
	Version           string `mapstructure:"version"`
}

// Proxy defines policy-owned runtime and request mutation settings.
type Proxy struct {
	NoProxy              []string    `mapstructure:"no_proxy"`
	SetLowercaseProxyEnv bool        `mapstructure:"set_lowercase_proxy_env"`
	Secrets              []string    `mapstructure:"secrets"`
	Env                  []ProxyEnv  `mapstructure:"env"`
	Rules                []ProxyRule `mapstructure:"rule"`
}

// ProxyEnv defines one command env var backed by a proxy-managed secret.
type ProxyEnv struct {
	Name   string   `mapstructure:"name"`
	Secret string   `mapstructure:"secret"`
	Rules  []string `mapstructure:"rules"`
	In     []string `mapstructure:"in"`
}

// ProxyRule defines host/path matches and request mutations.
type ProxyRule struct {
	Name               string                        `mapstructure:"name"`
	MatchHosts         []string                      `mapstructure:"match_hosts"`
	MatchPathPrefixes  []string                      `mapstructure:"match_path_prefixes"`
	SetHeader          map[string]string             `mapstructure:"set_header"`
	SetBasicAuth       *ProxyBasicAuth               `mapstructure:"set_basic_auth"`
	ReplacePlaceholder []ProxyPlaceholderReplacement `mapstructure:"replace_placeholder"`
}

// ProxyBasicAuth injects an Authorization Basic header from a managed secret.
type ProxyBasicAuth struct {
	Username string `mapstructure:"username"`
	Secret   string `mapstructure:"secret"`
}

// ProxyPlaceholderReplacement replaces placeholders with secret values.
type ProxyPlaceholderReplacement struct {
	Placeholder string   `mapstructure:"placeholder"`
	Secret      string   `mapstructure:"secret"`
	In          []string `mapstructure:"in"`
}

// Runtime is the resolved proxy-service runtime configuration.
type Runtime struct {
	ServiceVersion       string
	AdminListen          string
	ProxyListen          string
	AdvertiseProxyURL    string
	StateDir             string
	NoProxy              string
	SetLowercaseProxyEnv bool
	SecretValues         map[string]string
	EnvValues            map[string]string
	Rules                []ProxyRule
	PolicyRevision       string
}

const (
	defaultServiceVersion = "dev"
)

var (
	defaultNoProxy          = []string{"localhost", "127.0.0.1", "::1"}
	proxySecretAliasRE      = regexp.MustCompile(`^[a-z0-9_-]+$`)
	proxyEnvNameRE          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	stablePlaceholderPrefix = "__Q15_PROXY_ENV_"
)

// Load reads and validates the config file at path.
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

// LoadRuntime resolves config at path into service-owned runtime values.
func LoadRuntime(path string) (Runtime, error) {
	cfg, err := Load(path)
	if err != nil {
		return Runtime{}, err
	}
	return cfg.ResolveRuntime()
}

// Validate checks that the config is internally consistent.
func (c Config) Validate() error {
	if err := validateService(c.Service); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	if err := validateProxy(c.Proxy); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	return nil
}

// ResolveRuntime resolves the config into runtime state and secret values.
func (c Config) ResolveRuntime() (Runtime, error) {
	if err := c.Validate(); err != nil {
		return Runtime{}, err
	}

	secretAliases, err := normalizeProxySecretAliases(c.Proxy.Secrets)
	if err != nil {
		return Runtime{}, fmt.Errorf("proxy.secrets: %w", err)
	}
	secretValues := make(map[string]string, len(secretAliases))
	for _, alias := range secretAliases {
		envName := proxySecretEnvName(alias)
		value := strings.TrimSpace(os.Getenv(envName))
		if value == "" {
			return Runtime{}, fmt.Errorf(`proxy secret %q requires env var %q`, alias, envName)
		}
		secretValues[alias] = value
	}

	normalizedRules := normalizeProxyRules(c.Proxy.Rules)
	normalizedEnv := normalizeProxyEnv(c.Proxy.Env)
	rules, envValues, err := resolveProxyRuleRuntime(normalizedRules, normalizedEnv)
	if err != nil {
		return Runtime{}, err
	}

	serviceVersion := strings.TrimSpace(c.Service.Version)
	if serviceVersion == "" {
		serviceVersion = defaultServiceVersion
	}
	stateDir := filepath.Clean(strings.TrimSpace(c.Service.StateDir))
	revision, err := calculatePolicyRevision(policyRevisionInput{
		NoProxy:              normalizeStringList(c.Proxy.NoProxy, false),
		SetLowercaseProxyEnv: c.Proxy.SetLowercaseProxyEnv,
		Secrets:              secretAliases,
		Env:                  normalizedEnv,
		Rules:                rules,
		EnvValues:            envValues,
	})
	if err != nil {
		return Runtime{}, fmt.Errorf("calculate policy revision: %w", err)
	}

	return Runtime{
		ServiceVersion:       serviceVersion,
		AdminListen:          strings.TrimSpace(c.Service.AdminListen),
		ProxyListen:          strings.TrimSpace(c.Service.ProxyListen),
		AdvertiseProxyURL:    strings.TrimSpace(c.Service.AdvertiseProxyURL),
		StateDir:             stateDir,
		NoProxy:              strings.Join(resolveNoProxy(c.Proxy.NoProxy), ","),
		SetLowercaseProxyEnv: c.Proxy.SetLowercaseProxyEnv,
		SecretValues:         secretValues,
		EnvValues:            envValues,
		Rules:                rules,
		PolicyRevision:       revision,
	}, nil
}

func validateService(cfg Service) error {
	if strings.TrimSpace(cfg.AdminListen) == "" {
		return errors.New("admin_listen is required")
	}
	if strings.TrimSpace(cfg.ProxyListen) == "" {
		return errors.New("proxy_listen is required")
	}
	if strings.TrimSpace(cfg.AdvertiseProxyURL) == "" {
		return errors.New("advertise_proxy_url is required")
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		return errors.New("state_dir is required")
	}
	return nil
}

func validateProxy(proxy Proxy) error {
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

		hasAuthorizationHeader := false
		for headerName := range rule.SetHeader {
			trimmedName := strings.TrimSpace(headerName)
			if trimmedName == "" {
				return fmt.Errorf("rule[%d].set_header contains an empty header name", i)
			}
			if textproto.CanonicalMIMEHeaderKey(trimmedName) == "Authorization" {
				hasAuthorizationHeader = true
			}
		}
		if rule.SetBasicAuth != nil {
			if strings.TrimSpace(rule.SetBasicAuth.Username) == "" {
				return fmt.Errorf("rule[%d].set_basic_auth.username is required", i)
			}
			secretAlias, err := normalizeProxySecretAlias(rule.SetBasicAuth.Secret)
			if err != nil {
				return fmt.Errorf("rule[%d].set_basic_auth.secret: %w", i, err)
			}
			if _, ok := secretEnvByAlias[secretAlias]; !ok {
				return fmt.Errorf(
					"rule[%d].set_basic_auth.secret %q is not defined in proxy.secrets",
					i,
					secretAlias,
				)
			}
			if hasAuthorizationHeader {
				return fmt.Errorf(
					"rule[%d] cannot set both set_basic_auth and set_header.Authorization",
					i,
				)
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

	ruleNameCounts := make(map[string]int, len(proxy.Rules))
	for _, rule := range proxy.Rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			continue
		}
		ruleNameCounts[name]++
	}

	seenEnvNames := make(map[string]struct{}, len(proxy.Env))
	for i, env := range proxy.Env {
		name, err := normalizeProxyEnvName(env.Name)
		if err != nil {
			return fmt.Errorf("env[%d].name: %w", i, err)
		}
		if _, ok := seenEnvNames[name]; ok {
			return fmt.Errorf("env[%d].name duplicates %q", i, name)
		}
		seenEnvNames[name] = struct{}{}

		secretAlias, err := normalizeProxySecretAlias(env.Secret)
		if err != nil {
			return fmt.Errorf("env[%d].secret: %w", i, err)
		}
		if _, ok := secretEnvByAlias[secretAlias]; !ok {
			return fmt.Errorf("env[%d].secret %q is not defined in proxy.secrets", i, secretAlias)
		}

		rules := normalizeStringList(env.Rules, false)
		if len(rules) == 0 {
			return fmt.Errorf("env[%d].rules must contain at least one rule name", i)
		}
		for j, ruleName := range rules {
			count := ruleNameCounts[ruleName]
			switch {
			case count == 0:
				return fmt.Errorf(
					"env[%d].rules[%d] %q does not match any proxy rule name",
					i,
					j,
					ruleName,
				)
			case count > 1:
				return fmt.Errorf(
					"env[%d].rules[%d] %q matches multiple proxy rules; rule names must be unique when referenced by proxy.env",
					i,
					j,
					ruleName,
				)
			}
		}

		if len(env.In) == 0 {
			continue
		}
		for j, where := range env.In {
			where = strings.ToLower(strings.TrimSpace(where))
			switch where {
			case "header", "query", "path":
			case "body":
				return fmt.Errorf("env[%d].in[%d]=body is not supported in v1", i, j)
			default:
				return fmt.Errorf("env[%d].in[%d] must be header, query, or path", i, j)
			}
		}
	}

	return nil
}

func resolveNoProxy(values []string) []string {
	normalized := normalizeStringList(values, false)
	if len(normalized) == 0 {
		return append([]string(nil), defaultNoProxy...)
	}
	return normalized
}

func normalizeProxyRules(rules []ProxyRule) []ProxyRule {
	if len(rules) == 0 {
		return nil
	}

	out := make([]ProxyRule, 0, len(rules))
	for _, rule := range rules {
		normalizedRule := ProxyRule{
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
		if rule.SetBasicAuth != nil {
			normalizedRule.SetBasicAuth = &ProxyBasicAuth{
				Username: strings.TrimSpace(rule.SetBasicAuth.Username),
				Secret:   strings.ToLower(strings.TrimSpace(rule.SetBasicAuth.Secret)),
			}
		}
		if len(rule.ReplacePlaceholder) > 0 {
			normalizedRule.ReplacePlaceholder = make(
				[]ProxyPlaceholderReplacement,
				0,
				len(rule.ReplacePlaceholder),
			)
			for _, repl := range rule.ReplacePlaceholder {
				normalizedRule.ReplacePlaceholder = append(
					normalizedRule.ReplacePlaceholder,
					ProxyPlaceholderReplacement{
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

func normalizeProxyEnv(values []ProxyEnv) []ProxyEnv {
	if len(values) == 0 {
		return nil
	}

	out := make([]ProxyEnv, 0, len(values))
	for _, env := range values {
		normalized := ProxyEnv{
			Name:   strings.TrimSpace(env.Name),
			Secret: strings.ToLower(strings.TrimSpace(env.Secret)),
			Rules:  normalizeStringList(env.Rules, false),
			In:     normalizeStringList(env.In, true),
		}
		if len(normalized.In) == 0 {
			normalized.In = []string{"header"}
		}
		out = append(out, normalized)
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

func resolveProxyRuleRuntime(
	rules []ProxyRule,
	envMappings []ProxyEnv,
) ([]ProxyRule, map[string]string, error) {
	if len(envMappings) == 0 {
		return rules, nil, nil
	}

	ruleIndexByName := make(map[string]int, len(rules))
	for i, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			continue
		}
		ruleIndexByName[name] = i
	}

	envValues := make(map[string]string, len(envMappings))
	for _, env := range envMappings {
		placeholder, err := stableProxyPlaceholder(env)
		if err != nil {
			return nil, nil, fmt.Errorf("generate placeholder for env %q: %w", env.Name, err)
		}
		envValues[env.Name] = placeholder

		replacement := ProxyPlaceholderReplacement{
			Placeholder: placeholder,
			Secret:      env.Secret,
			In:          append([]string(nil), env.In...),
		}
		for _, ruleName := range env.Rules {
			idx, ok := ruleIndexByName[ruleName]
			if !ok {
				return nil, nil, fmt.Errorf(
					"env %q references missing proxy rule %q",
					env.Name,
					ruleName,
				)
			}
			rules[idx].ReplacePlaceholder = append(rules[idx].ReplacePlaceholder, replacement)
		}
	}

	return rules, envValues, nil
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

func normalizeProxyEnvName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("must not be empty")
	}
	if !proxyEnvNameRE.MatchString(name) {
		return "", errors.New(
			"must start with a letter or underscore and contain only letters, digits, or underscores",
		)
	}
	return name, nil
}

func proxySecretEnvName(alias string) string {
	alias = strings.ReplaceAll(alias, "-", "_")
	return strings.ToUpper(alias)
}

func stableProxyPlaceholder(env ProxyEnv) (string, error) {
	payload, err := json.Marshal(struct {
		Name   string   `json:"name"`
		Secret string   `json:"secret"`
		Rules  []string `json:"rules"`
		In     []string `json:"in"`
	}{
		Name:   env.Name,
		Secret: env.Secret,
		Rules:  env.Rules,
		In:     env.In,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return stablePlaceholderPrefix + hex.EncodeToString(sum[:16]) + "__", nil
}

type policyRevisionInput struct {
	NoProxy              []string          `json:"no_proxy"`
	SetLowercaseProxyEnv bool              `json:"set_lowercase_proxy_env"`
	Secrets              []string          `json:"secrets"`
	Env                  []ProxyEnv        `json:"env"`
	Rules                []ProxyRule       `json:"rules"`
	EnvValues            map[string]string `json:"env_values"`
}

func calculatePolicyRevision(value policyRevisionInput) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
