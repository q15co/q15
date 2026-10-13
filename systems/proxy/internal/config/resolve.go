package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ResolveRuntime resolves a policy into runtime state and secret values.
//
// It works from the policy's canonical form, so what the proxy serves is what
// the file means rather than what this build happens to read into it: the
// headers it matches, the placeholders it substitutes and the revision it
// reports all come from one normalization, shared with the CLI that wrote the
// file.
func ResolveRuntime(cfg Config) (Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return Runtime{}, err
	}

	secretAliases, err := cfg.Proxy.NormalizedSecrets()
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

	normalizedRules := cfg.Proxy.NormalizedRules()
	normalizedEnv := cfg.Proxy.NormalizedEnv()
	rules, envValues, err := resolveProxyRuleRuntime(normalizedRules, normalizedEnv)
	if err != nil {
		return Runtime{}, err
	}

	revision, err := calculatePolicyRevision(policyRevisionInput{
		NoProxy:              cfg.Proxy.NormalizedNoProxy(),
		SetLowercaseProxyEnv: cfg.Proxy.SetLowercaseProxyEnv,
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
		NoProxy:              strings.Join(resolveNoProxy(cfg.Proxy.NormalizedNoProxy()), ","),
		SetLowercaseProxyEnv: cfg.Proxy.SetLowercaseProxyEnv,
		SecretValues:         secretValues,
		EnvValues:            envValues,
		Rules:                rules,
		PolicyRevision:       revision,
	}, nil
}

func resolveNoProxy(normalized []string) []string {
	if len(normalized) == 0 {
		return append([]string(nil), defaultNoProxy...)
	}
	return normalized
}

// resolveProxyRuleRuntime turns the declared environment mappings into the
// request mutations the proxy applies: each mapping becomes one placeholder,
// substituted wherever the rules that reference it say a secret belongs.
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

// proxySecretEnvName is the environment variable a secret alias is read from:
// the proxy's own convention, uppercased, with dashes read as underscores.
func proxySecretEnvName(alias string) string {
	alias = strings.ReplaceAll(alias, "-", "_")
	return strings.ToUpper(alias)
}

// stableProxyPlaceholder derives the placeholder a mapping substitutes. It is a
// function of the mapping alone, so the same policy resolves to the same
// placeholder on every start, and a revision computed from it only moves when
// the policy does.
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

// stablePlaceholderPrefix marks a value the proxy substitutes for a secret. It
// is a literal rather than a random token so that a placeholder computed twice
// matches, which is what lets the policy revision be stable.
const stablePlaceholderPrefix = "__Q15_PROXY_ENV_"

type policyRevisionInput struct {
	NoProxy              []string          `json:"no_proxy"`
	SetLowercaseProxyEnv bool              `json:"set_lowercase_proxy_env"`
	Secrets              []string          `json:"secrets"`
	Env                  []ProxyEnv        `json:"env"`
	Rules                []ProxyRule       `json:"rules"`
	EnvValues            map[string]string `json:"env_values"`
}

// calculatePolicyRevision fingerprints everything the proxy enforces, so a
// client can tell that the policy it is talking to changed without asking what
// it changed to.
func calculatePolicyRevision(value policyRevisionInput) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
