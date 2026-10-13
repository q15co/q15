// Package config loads and resolves q15-proxy configuration.
//
// The policy file's schema is not defined here. It lives in
// libs/proxy-contract/policy, because the CLI that renders a policy for a stack
// lives in the agent module and Go's internal/ rule puts this package out of its
// reach. What stays here is the proxy's own half of that contract: the path the
// policy is read from, what the environment resolves it to, and the rule set the
// servers serve.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/libs/proxy-contract/policy"
)

// Config is a q15-proxy policy, as libs/proxy-contract/policy defines it. The
// names here are aliases rather than new types, so a policy the CLI rendered is a
// policy this package reads, with no conversion for a call site to get wrong.
type Config = policy.Config

// Proxy holds the policy's runtime and request mutation settings.
type Proxy = policy.Proxy

// ProxyEnv is one command environment variable backed by a managed secret.
type ProxyEnv = policy.ProxyEnv

// ProxyRule is one host and path match with the mutations it applies.
type ProxyRule = policy.ProxyRule

// ProxyBasicAuth injects an Authorization Basic header from a managed secret.
type ProxyBasicAuth = policy.ProxyBasicAuth

// ProxyPlaceholderReplacement replaces a placeholder with a secret value.
type ProxyPlaceholderReplacement = policy.ProxyPlaceholderReplacement

// Runtime is the resolved q15-proxy runtime configuration.
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
	runtimeAdvertiseProxyURL = "http://q15-proxy:18080"
	runtimeStateDir          = "/var/lib/q15/proxy"
)

// defaultNoProxy is the bypass list a policy that names none gets. Traffic to
// loopback never leaves the machine, so sending it through a forward proxy that
// is itself on loopback is a loop waiting to happen.
var defaultNoProxy = []string{"localhost", "127.0.0.1", "::1"}

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

	cfg, err := policy.Decode(data)
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return cfg, nil
}

// LoadRuntime resolves the policy at path into runtime values.
func LoadRuntime(path string) (Runtime, error) {
	cfg, err := Load(path)
	if err != nil {
		return Runtime{}, err
	}
	return ResolveRuntime(cfg)
}
