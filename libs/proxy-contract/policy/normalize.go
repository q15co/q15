package policy

import (
	"errors"
	"fmt"
	"net/textproto"
	"strings"
)

// The canonical form of a policy is the format's own, not a convenience of one
// reader: surrounding whitespace carries no meaning, a secret alias and the
// places a placeholder is substituted are the same in either case, an empty
// entry is no entry, and a repeated entry is one entry. Both sides of the
// contract take that form here, so a policy means the same thing to the CLI
// that rendered it and to the proxy that enforces it.

// NormalizedNoProxy returns the hosts that bypass the proxy. An empty result
// means the file named none, which is not the same as naming no hosts: the
// runtime decides what an absent list falls back to.
func (p Proxy) NormalizedNoProxy() []string {
	return normalizeList(p.NoProxy, false)
}

// NormalizedSecrets returns the secret aliases a policy allows, in the order
// they are declared. An alias is lower-cased and restricted to a-z, 0-9, _
// and -, so a policy cannot name two secrets that differ only in case.
func (p Proxy) NormalizedSecrets() ([]string, error) {
	if len(p.Secrets) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(p.Secrets))
	seen := make(map[string]struct{}, len(p.Secrets))
	for i, alias := range p.Secrets {
		normalized, err := normalizeAlias(alias)
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

// NormalizedRules returns the request mutation rules in canonical form: header
// names in their wire case, host and path matches trimmed, and the secret a
// rule names lower-cased so it matches the alias it refers to.
func (p Proxy) NormalizedRules() []ProxyRule {
	if len(p.Rules) == 0 {
		return nil
	}

	out := make([]ProxyRule, 0, len(p.Rules))
	for _, rule := range p.Rules {
		normalizedRule := ProxyRule{
			Name:              strings.TrimSpace(rule.Name),
			MatchHosts:        normalizeList(rule.MatchHosts, true),
			MatchPathPrefixes: normalizeList(rule.MatchPathPrefixes, false),
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
						In:          normalizeList(repl.In, true),
					},
				)
			}
		}
		out = append(out, normalizedRule)
	}
	return out
}

// NormalizedEnv returns the command environment mappings in canonical form.
// An entry that names no place for its value falls back to the request header,
// which is where a bearer token belongs and what a policy that says only
// "GH_TOKEN comes from github_token" means.
func (p Proxy) NormalizedEnv() []ProxyEnv {
	if len(p.Env) == 0 {
		return nil
	}

	out := make([]ProxyEnv, 0, len(p.Env))
	for _, env := range p.Env {
		normalized := ProxyEnv{
			Name:   strings.TrimSpace(env.Name),
			Secret: strings.ToLower(strings.TrimSpace(env.Secret)),
			Rules:  normalizeList(env.Rules, false),
			In:     normalizeList(env.In, true),
		}
		if len(normalized.In) == 0 {
			normalized.In = []string{"header"}
		}
		out = append(out, normalized)
	}
	return out
}

// normalizeList trims each entry, drops the empty ones and drops repeats,
// lower-casing when the format is case-insensitive about them.
func normalizeList(values []string, lower bool) []string {
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

func normalizeAlias(alias string) (string, error) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return "", errors.New("alias must not be empty")
	}
	if !secretAliasRE.MatchString(alias) {
		return "", errors.New("alias must contain only a-z, 0-9, _, or -")
	}
	return alias, nil
}

func normalizeEnvName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("must not be empty")
	}
	if !envNameRE.MatchString(name) {
		return "", errors.New(
			"must start with a letter or underscore and contain only letters, digits, or underscores",
		)
	}
	return name, nil
}
