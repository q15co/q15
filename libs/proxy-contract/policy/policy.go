// Package policy defines the q15-proxy policy file: the schema the proxy
// enforces, the strict decode that reads it, and the canonical form both the
// proxy and the CLI work from.
//
// It lives under libs/ rather than beside the proxy's own loader for a
// structural reason. Go's internal/ rule makes systems/proxy/internal/config
// unreachable from systems/agent, where the CLI lives, so a policy rendered
// there could only ever be a second, hand-copied template - exactly the drift
// the command line's fold into the agent module exists to prevent. What stays
// with the proxy is what is genuinely the proxy's own: the path it reads, the
// values it resolves out of the environment, and the rule set its servers
// serve.
package policy

import (
	"bytes"
	"errors"
	"io"

	"go.yaml.in/yaml/v3"
)

// SchemaVersion is the policy schema version this package reads and writes.
// Like the agent's configuration, the field is optional on read - a policy
// written before it existed is a version-1 policy - and a policy written by a
// build that understands a different shape is refused by name. A policy that is
// half applied is a proxy whose rules nobody can reason about, so the file
// carries its shape from the first install rather than after the first break.
const SchemaVersion = 1

// Config is the top-level structure loaded from the proxy policy file.
type Config struct {
	// SchemaVersion is the policy schema version. Absent reads as the current
	// version; a newer one is refused rather than partly understood. See
	// Validate.
	SchemaVersion int `yaml:"schema_version,omitempty"`

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

// ProxyRule defines host/path matches and request mutations. The mutations are
// optional, so a rule that only matches reads as a rule that only matches rather
// than as one carrying four empty collections.
type ProxyRule struct {
	Name               string                        `yaml:"name"`
	MatchHosts         []string                      `yaml:"match_hosts"`
	MatchPathPrefixes  []string                      `yaml:"match_path_prefixes,omitempty"`
	SetHeader          map[string]string             `yaml:"set_header,omitempty"`
	SetBasicAuth       *ProxyBasicAuth               `yaml:"set_basic_auth,omitempty"`
	ReplacePlaceholder []ProxyPlaceholderReplacement `yaml:"replace_placeholder,omitempty"`
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

// Decode parses exactly one policy document. Unknown fields and a second
// document are refused rather than ignored: the policy file is the whole of what
// the proxy enforces, so a key nothing reads is a rule somebody believes is in
// effect.
//
// Decode does not validate; call Validate on the result. An empty document
// decodes to an empty policy, which validation then refuses, so the two steps
// report the same failure a person would expect from an empty file.
func Decode(data []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}
	if err := ensureSingleDocument(dec); err != nil {
		return Config{}, err
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
