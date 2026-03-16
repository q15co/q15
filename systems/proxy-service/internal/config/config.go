// Package config loads, validates, and resolves q15 proxy-service configuration.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the top-level structure loaded from the proxy policy file.
type Config struct {
	Proxy Proxy `yaml:"proxy"`
}

// Proxy defines policy-owned runtime and request mutation settings.
type Proxy struct {
	NoProxy              []string    `yaml:"no_proxy"`
	SetLowercaseProxyEnv bool        `yaml:"set_lowercase_proxy_env"`
	Secrets              []string    `yaml:"secrets"`
	Env                  []ProxyEnv  `yaml:"env"`
	Rules                []ProxyRule `yaml:"rules"`
}

// ProxyEnv defines one command env var backed by a proxy-managed secret.
type ProxyEnv struct {
	Name   string   `yaml:"name"`
	Secret string   `yaml:"secret"`
	Rules  []string `yaml:"rules"`
	In     []string `yaml:"in"`
}

// ProxyRule defines host/path matches and request mutations.
type ProxyRule struct {
	Name               string                        `yaml:"name"`
	MatchHosts         []string                      `yaml:"match_hosts"`
	MatchPathPrefixes  []string                      `yaml:"match_path_prefixes"`
	SetHeader          map[string]string             `yaml:"set_header"`
	SetBasicAuth       *ProxyBasicAuth               `yaml:"set_basic_auth"`
	ReplacePlaceholder []ProxyPlaceholderReplacement `yaml:"replace_placeholder"`
}

// ProxyBasicAuth injects an Authorization Basic header from a managed secret.
type ProxyBasicAuth struct {
	Username string `yaml:"username"`
	Secret   string `yaml:"secret"`
}

// ProxyPlaceholderReplacement replaces placeholders with secret values.
type ProxyPlaceholderReplacement struct {
	Placeholder string   `yaml:"placeholder"`
	Secret      string   `yaml:"secret"`
	In          []string `yaml:"in"`
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
	defaultServiceVersion    = "dev"
	runtimeAdminListen       = ":50052"
	runtimeProxyListen       = ":18080"
	runtimeAdvertiseProxyURL = "http://q15-proxy-service:18080"
	runtimeStateDir          = "/var/lib/q15/proxy-service"
)

var (
	defaultNoProxy          = []string{"localhost", "127.0.0.1", "::1"}
	proxySecretAliasRE      = regexp.MustCompile(`^[a-z0-9_-]+$`)
	proxyEnvNameRE          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	stablePlaceholderPrefix = "__Q15_PROXY_ENV_"
)

// Load reads and validates the proxy policy file at path.
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

// LoadRuntime resolves config at path into runtime values.
func LoadRuntime(path string) (Runtime, error) {
	cfg, err := Load(path)
	if err != nil {
		return Runtime{}, err
	}
	return cfg.ResolveRuntime()
}

// Validate checks that the config is internally consistent.
func (c Config) Validate() error {
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
		value, err := resolveSecretEnvValue(envName)
		if err != nil {
			return Runtime{}, fmt.Errorf(`proxy secret %q requires %w`, alias, err)
		}
		secretValues[alias] = value
	}

	normalizedRules := normalizeProxyRules(c.Proxy.Rules)
	normalizedEnv := normalizeProxyEnv(c.Proxy.Env)
	rules, envValues, err := resolveProxyRuleRuntime(normalizedRules, normalizedEnv)
	if err != nil {
		return Runtime{}, err
	}

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
		ServiceVersion:       defaultServiceVersion,
		AdminListen:          runtimeAdminListen,
		ProxyListen:          runtimeProxyListen,
		AdvertiseProxyURL:    runtimeAdvertiseProxyURL,
		StateDir:             runtimeStateDir,
		NoProxy:              strings.Join(resolveNoProxy(c.Proxy.NoProxy), ","),
		SetLowercaseProxyEnv: c.Proxy.SetLowercaseProxyEnv,
		SecretValues:         secretValues,
		EnvValues:            envValues,
		Rules:                rules,
		PolicyRevision:       revision,
	}, nil
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
			return fmt.Errorf("rules[%d].match_hosts must contain at least one host", i)
		}
		for j, host := range rule.MatchHosts {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("rules[%d].match_hosts[%d] must not be empty", i, j)
			}
		}
		for j, pfx := range rule.MatchPathPrefixes {
			pfx = strings.TrimSpace(pfx)
			if pfx == "" {
				return fmt.Errorf("rules[%d].match_path_prefixes[%d] must not be empty", i, j)
			}
			if !strings.HasPrefix(pfx, "/") {
				return fmt.Errorf("rules[%d].match_path_prefixes[%d] must start with /", i, j)
			}
		}

		hasAuthorizationHeader := false
		for headerName := range rule.SetHeader {
			trimmedName := strings.TrimSpace(headerName)
			if trimmedName == "" {
				return fmt.Errorf("rules[%d].set_header contains an empty header name", i)
			}
			if textproto.CanonicalMIMEHeaderKey(trimmedName) == "Authorization" {
				hasAuthorizationHeader = true
			}
		}
		if rule.SetBasicAuth != nil {
			if strings.TrimSpace(rule.SetBasicAuth.Username) == "" {
				return fmt.Errorf("rules[%d].set_basic_auth.username is required", i)
			}
			secretAlias, err := normalizeProxySecretAlias(rule.SetBasicAuth.Secret)
			if err != nil {
				return fmt.Errorf("rules[%d].set_basic_auth.secret: %w", i, err)
			}
			if _, ok := secretEnvByAlias[secretAlias]; !ok {
				return fmt.Errorf(
					"rules[%d].set_basic_auth.secret %q is not defined in proxy.secrets",
					i,
					secretAlias,
				)
			}
			if hasAuthorizationHeader {
				return fmt.Errorf(
					"rules[%d] cannot set both set_basic_auth and set_header.Authorization",
					i,
				)
			}
		}

		for j, repl := range rule.ReplacePlaceholder {
			if strings.TrimSpace(repl.Placeholder) == "" {
				return fmt.Errorf("rules[%d].replace_placeholder[%d].placeholder is required", i, j)
			}
			secretAlias, err := normalizeProxySecretAlias(repl.Secret)
			if err != nil {
				return fmt.Errorf("rules[%d].replace_placeholder[%d].secret: %w", i, j, err)
			}
			if _, ok := secretEnvByAlias[secretAlias]; !ok {
				return fmt.Errorf(
					"rules[%d].replace_placeholder[%d].secret %q is not defined in proxy.secrets",
					i,
					j,
					secretAlias,
				)
			}
			if len(repl.In) == 0 {
				return fmt.Errorf("rules[%d].replace_placeholder[%d].in must not be empty", i, j)
			}
			for k, where := range repl.In {
				where = strings.ToLower(strings.TrimSpace(where))
				switch where {
				case "header", "query", "path":
				case "body":
					return fmt.Errorf(
						"rules[%d].replace_placeholder[%d].in[%d]=body is not supported in v1",
						i,
						j,
						k,
					)
				default:
					return fmt.Errorf(
						"rules[%d].replace_placeholder[%d].in[%d] must be header, query, or path",
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
