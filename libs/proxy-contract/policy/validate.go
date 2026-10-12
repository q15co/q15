package policy

import (
	"errors"
	"fmt"
	"net/textproto"
	"regexp"
	"strings"
)

var (
	secretAliasRE = regexp.MustCompile(`^[a-z0-9_-]+$`)
	envNameRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Validate checks that the policy is internally consistent: that the schema
// version is one this build reads, that every rule can match something, and
// that every secret a rule or an environment mapping names is declared. It is
// deliberately about the shape of the file and not about the values: whether a
// declared secret has a value in this environment is a question for the runtime,
// and a policy that fails it here would be a file the CLI refuses to render.
func (c Config) Validate() error {
	if err := checkSchemaVersion(c.SchemaVersion, SchemaVersion); err != nil {
		return err
	}
	return validateProxy(c.Proxy)
}

// checkSchemaVersion reports whether this build can read a policy written at
// version have. An absent version is the current one; a newer one is refused by
// name rather than partly understood; an older one is refused too, because the
// package offers no migration and guessing one would apply a policy nobody
// wrote.
func checkSchemaVersion(have, want int) error {
	switch {
	case have == 0 || have == want:
		return nil
	case have > want:
		return fmt.Errorf(
			"policy schema version %d is newer than this build reads (highest %d)",
			have,
			want,
		)
	case have < 0:
		return fmt.Errorf("policy schema version %d is not a valid version", have)
	default:
		return fmt.Errorf(
			"policy schema version %d is older than this build reads (%d): migrate the policy before loading it",
			have,
			want,
		)
	}
}

// validateProxy validates the policy against its own canonical form, so a
// policy that is accepted here resolves to the same rules the proxy serves.
func validateProxy(proxy Proxy) error {
	secretAliases, err := proxy.NormalizedSecrets()
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	if len(secretAliases) == 0 {
		return errors.New("secrets must contain at least one alias")
	}
	declared := make(map[string]struct{}, len(secretAliases))
	for _, alias := range secretAliases {
		declared[alias] = struct{}{}
	}

	rules := proxy.NormalizedRules()
	for i, rule := range rules {
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
			secretAlias, err := normalizeAlias(rule.SetBasicAuth.Secret)
			if err != nil {
				return fmt.Errorf("rules[%d].set_basic_auth.secret: %w", i, err)
			}
			if _, ok := declared[secretAlias]; !ok {
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
			secretAlias, err := normalizeAlias(repl.Secret)
			if err != nil {
				return fmt.Errorf("rules[%d].replace_placeholder[%d].secret: %w", i, j, err)
			}
			if _, ok := declared[secretAlias]; !ok {
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

	ruleNameCounts := make(map[string]int, len(rules))
	for _, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			continue
		}
		ruleNameCounts[name]++
	}

	env := proxy.NormalizedEnv()
	seenEnvNames := make(map[string]struct{}, len(env))
	for i, mapping := range env {
		name, err := normalizeEnvName(mapping.Name)
		if err != nil {
			return fmt.Errorf("env[%d].name: %w", i, err)
		}
		if _, ok := seenEnvNames[name]; ok {
			return fmt.Errorf("env[%d].name duplicates %q", i, name)
		}
		seenEnvNames[name] = struct{}{}

		secretAlias, err := normalizeAlias(mapping.Secret)
		if err != nil {
			return fmt.Errorf("env[%d].secret: %w", i, err)
		}
		if _, ok := declared[secretAlias]; !ok {
			return fmt.Errorf("env[%d].secret %q is not defined in proxy.secrets", i, secretAlias)
		}

		if len(mapping.Rules) == 0 {
			return fmt.Errorf("env[%d].rules must contain at least one rule name", i)
		}
		for j, ruleName := range mapping.Rules {
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

		if len(mapping.In) == 0 {
			continue
		}
		for j, where := range mapping.In {
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
