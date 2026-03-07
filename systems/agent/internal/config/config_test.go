package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentRuntimes_TOML(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

	[[agent]]
	name = "Jared"
	models = ["moonshot/kimi-k2.5", "moonshot/kimi-k2"]

	[agent.sandbox]
	container_name = "q15-jared"
	workspace_host_dir = "/tmp/q15-workspaces/jared"
	workspace_dir = "/workspace"

	[agent.telegram]
	token_env = "JARED_TELEGRAM_TOKEN"
	allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("load runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}

	rt := runtimes[0]
	if rt.Name != "Jared" {
		t.Fatalf("unexpected agent name: %q", rt.Name)
	}
	if len(rt.Models) != 2 {
		t.Fatalf("unexpected models: %#v", rt.Models)
	}
	if rt.Models[0].Ref != "moonshot/kimi-k2.5" || rt.Models[0].ModelName != "kimi-k2.5" {
		t.Fatalf("unexpected first model runtime: %#v", rt.Models[0])
	}
	if rt.Models[0].ProviderType != "openai-compatible" {
		t.Fatalf("unexpected first model provider type: %q", rt.Models[0].ProviderType)
	}
	if rt.Models[0].ProviderBaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("unexpected first model provider base url: %q", rt.Models[0].ProviderBaseURL)
	}
	if rt.Models[0].ProviderAPIKey != "api-123" {
		t.Fatalf("unexpected first model provider api key: %q", rt.Models[0].ProviderAPIKey)
	}
	if rt.Models[1].Ref != "moonshot/kimi-k2" || rt.Models[1].ModelName != "kimi-k2" {
		t.Fatalf("unexpected second model runtime: %#v", rt.Models[1])
	}
	if rt.SandboxContainerName != "q15-jared" {
		t.Fatalf("unexpected sandbox container name: %q", rt.SandboxContainerName)
	}
	if rt.WorkspaceHostDir != "/tmp/q15-workspaces/jared" {
		t.Fatalf("unexpected workspace host dir: %q", rt.WorkspaceHostDir)
	}
	if rt.WorkspaceDir != "/workspace" {
		t.Fatalf("unexpected workspace dir: %q", rt.WorkspaceDir)
	}
	if rt.MemoryHostDir != "/tmp/q15-workspaces/jared/.q15-memory" {
		t.Fatalf("unexpected memory host dir: %q", rt.MemoryHostDir)
	}
	if rt.MemoryDir != "/memory" {
		t.Fatalf("unexpected memory dir: %q", rt.MemoryDir)
	}
	if rt.MemoryRecentTurns != 6 {
		t.Fatalf("unexpected default memory recent turns: %d", rt.MemoryRecentTurns)
	}
	if rt.TelegramToken != "tg-123" {
		t.Fatalf("unexpected telegram token: %q", rt.TelegramToken)
	}
	if len(rt.TelegramAllowedUserIDs) != 1 || rt.TelegramAllowedUserIDs[0] != 123456789 {
		t.Fatalf("unexpected allowed telegram user ids: %#v", rt.TelegramAllowedUserIDs)
	}
}

func TestLoadAgentRuntimes_TOML_MemoryRecentTurnsPassThrough(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[agent]]
name = "Jared"
models = ["moonshot/kimi-k2.5"]
memory_recent_turns = 42

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("load runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}
	if got := runtimes[0].MemoryRecentTurns; got != 42 {
		t.Fatalf("unexpected memory recent turns: %d", got)
	}
}

func TestLoadAgentRuntimes_TOML_EmptyConfigReturnsNoRuntimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# starter config\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("load runtimes: %v", err)
	}
	if len(runtimes) != 0 {
		t.Fatalf("expected 0 runtimes, got %d", len(runtimes))
	}
}

func TestValidateRequiresProviderWhenAgentConfigured(t *testing.T) {
	cfg := Config{
		Agents: []Agent{
			{
				Name:   "missing-provider",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-missing-provider",
					WorkspaceHostDir: "/tmp/q15-workspaces/missing-provider",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error when providers are missing")
	}
	if !strings.Contains(err.Error(), "provider cannot be empty") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRequiresAgentModels(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name: "legacy",
				Sandbox: Sandbox{
					ContainerName:    "q15-legacy",
					WorkspaceHostDir: "/tmp/q15-workspaces/legacy",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when agent.models is missing")
	}
}

func TestValidateRequiresTelegramAllowedUserIDs(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "no-allowlist",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-no-allowlist",
					WorkspaceHostDir: "/tmp/q15-workspaces/no-allowlist",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv: "TEST_TELEGRAM_TOKEN",
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when telegram.allowed_user_ids is missing")
	}
}

func TestValidateRequiresBaseURLForOpenAICompatibleProvider(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:   "zai",
				Type:   "openai-compatible",
				KeyEnv: "ZAI_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "zai-agent",
				Models: []string{"zai/glm-4.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-zai",
					WorkspaceHostDir: "/tmp/q15-workspaces/zai",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when provider.base_url is missing")
	}
}

func TestResolveAgentRuntimesSupportsMixedProviderFallbacks(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-moonshot")
	t.Setenv("ZAI_API_KEY", "api-zai")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
			{
				Name:    "zai",
				Type:    "openai-compatible",
				BaseURL: "https://example.com/openai/v1",
				KeyEnv:  "ZAI_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "mixed-fallbacks",
				Models: []string{"moonshot/kimi-k2.5", "zai/glm-5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-mixed-fallbacks",
					WorkspaceHostDir: "/tmp/q15-workspaces/mixed-fallbacks",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	runtimes, err := cfg.ResolveAgentRuntimes()
	if err != nil {
		t.Fatalf("resolve runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}
	if len(runtimes[0].Models) != 2 {
		t.Fatalf("expected 2 resolved model fallbacks, got %#v", runtimes[0].Models)
	}
	if runtimes[0].Models[0].ProviderName != "moonshot" ||
		runtimes[0].Models[0].ModelName != "kimi-k2.5" {
		t.Fatalf("unexpected first fallback: %#v", runtimes[0].Models[0])
	}
	if runtimes[0].Models[1].ProviderName != "zai" || runtimes[0].Models[1].ModelName != "glm-5" {
		t.Fatalf("unexpected second fallback: %#v", runtimes[0].Models[1])
	}
}

func TestLoadAgentRuntimes_TOML_WithSandboxProxy(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")
	t.Setenv("GH_TOKEN", "ghp_test_123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[agent]]
name = "Jared"
models = ["moonshot/kimi-k2.5"]

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.sandbox.proxy]
secrets = ["gh_token"]

[[agent.sandbox.proxy.rule]]
name = "github-api"
match_hosts = ["api.github.com"]
match_path_prefixes = ["/"]
set_header = { Authorization = "token {{secret.gh_token}}" }

[[agent.sandbox.proxy.rule.replace_placeholder]]
placeholder = "__Q15_GH_TOKEN__"
secret = "gh_token"
in = ["header", "query", "path"]

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("load runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}

	rt := runtimes[0]
	if rt.SandboxProxy == nil {
		t.Fatalf("expected sandbox proxy runtime to be resolved")
	}
	if rt.SandboxProxy.ListenAddr != "0.0.0.0:0" {
		t.Fatalf("unexpected default proxy listen addr: %q", rt.SandboxProxy.ListenAddr)
	}
	if rt.SandboxProxy.ContainerProxyHost != "host.containers.internal" {
		t.Fatalf("unexpected container proxy host: %q", rt.SandboxProxy.ContainerProxyHost)
	}
	if rt.SandboxProxy.CACertContainerPath != "/run/q15-proxy/ca.crt" {
		t.Fatalf(
			"unexpected default CA cert container path: %q",
			rt.SandboxProxy.CACertContainerPath,
		)
	}
	if !rt.SandboxProxy.SetLowercaseProxyEnv {
		t.Fatalf("expected lowercase proxy env default to true")
	}
	if got := rt.SandboxProxy.SecretValues["gh_token"]; got != "ghp_test_123" {
		t.Fatalf("unexpected resolved proxy secret value: %q", got)
	}
	if len(rt.SandboxProxy.Rules) != 1 {
		t.Fatalf("unexpected proxy rules: %#v", rt.SandboxProxy.Rules)
	}
	rule := rt.SandboxProxy.Rules[0]
	if rule.Name != "github-api" {
		t.Fatalf("unexpected proxy rule name: %q", rule.Name)
	}
	if len(rule.MatchHosts) != 1 || rule.MatchHosts[0] != "api.github.com" {
		t.Fatalf("unexpected proxy match hosts: %#v", rule.MatchHosts)
	}
	if got := rule.SetHeader["Authorization"]; got != "token {{secret.gh_token}}" {
		t.Fatalf("unexpected auth header template: %q", got)
	}
	if len(rule.ReplacePlaceholder) != 1 {
		t.Fatalf("unexpected placeholder replacement config: %#v", rule.ReplacePlaceholder)
	}
	if got := rule.ReplacePlaceholder[0].In; len(got) != 3 ||
		got[0] != "header" || got[1] != "query" || got[2] != "path" {
		t.Fatalf("unexpected replacement locations: %#v", got)
	}
}

func TestLoadAgentRuntimes_TOML_WithSandboxProxyEnv(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")
	t.Setenv("JARED_GH_TOKEN", "ghp_test_123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[agent]]
name = "Jared"
models = ["moonshot/kimi-k2.5"]

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.sandbox.proxy]
secrets = ["jared_gh_token"]

[[agent.sandbox.proxy.rule]]
name = "github-api"
match_hosts = ["api.github.com"]
match_path_prefixes = ["/"]

[[agent.sandbox.proxy.env]]
name = "GH_TOKEN"
secret = "jared_gh_token"
rules = ["github-api"]

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("load runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}

	rt := runtimes[0]
	if rt.SandboxProxy == nil {
		t.Fatalf("expected sandbox proxy runtime to be resolved")
	}
	if got := rt.SandboxProxy.SecretValues["jared_gh_token"]; got != "ghp_test_123" {
		t.Fatalf("unexpected resolved proxy secret value: %q", got)
	}
	placeholder := rt.SandboxProxy.EnvValues["GH_TOKEN"]
	if placeholder == "" {
		t.Fatalf("expected GH_TOKEN placeholder env value")
	}
	if placeholder == "ghp_test_123" {
		t.Fatalf("expected placeholder env value to differ from real secret")
	}
	if len(placeholder) != 32 {
		t.Fatalf("unexpected placeholder length: got %d value %q", len(placeholder), placeholder)
	}
	for _, ch := range placeholder {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			t.Fatalf("expected opaque hex placeholder, got %q", placeholder)
		}
	}
	if len(rt.SandboxProxy.Rules) != 1 {
		t.Fatalf("unexpected proxy rules: %#v", rt.SandboxProxy.Rules)
	}
	rule := rt.SandboxProxy.Rules[0]
	if len(rule.ReplacePlaceholder) != 1 {
		t.Fatalf("expected expanded placeholder replacement, got %#v", rule.ReplacePlaceholder)
	}
	if got := rule.ReplacePlaceholder[0].Placeholder; got != placeholder {
		t.Fatalf("unexpected expanded placeholder: %q", got)
	}
	if got := rule.ReplacePlaceholder[0].Secret; got != "jared_gh_token" {
		t.Fatalf("unexpected expanded secret alias: %q", got)
	}
	if got := rule.ReplacePlaceholder[0].In; len(got) != 1 || got[0] != "header" {
		t.Fatalf("unexpected default replacement scope: %#v", got)
	}
}

func TestValidateRejectsSandboxProxyBodyPlaceholderReplacement(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "proxy-agent",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-proxy-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"gh_token"},
						Rules: []SandboxProxyRule{
							{
								Name:       "github-api",
								MatchHosts: []string{"api.github.com"},
								ReplacePlaceholder: []SandboxProxyPlaceholderReplacement{
									{
										Placeholder: "__Q15_GH_TOKEN__",
										Secret:      "gh_token",
										In:          []string{"body"},
									},
								},
							},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for body placeholder replacement in v1")
	}
}

func TestValidateRejectsInvalidSandboxProxyEnvConfig(t *testing.T) {
	baseConfig := func() Config {
		return Config{
			Providers: []Provider{
				{
					Name:    "moonshot",
					Type:    "openai-compatible",
					BaseURL: "https://api.moonshot.ai/v1",
					KeyEnv:  "MOONSHOT_API_KEY",
				},
			},
			Agents: []Agent{
				{
					Name:   "proxy-agent",
					Models: []string{"moonshot/kimi-k2.5"},
					Sandbox: Sandbox{
						ContainerName:    "q15-proxy-agent",
						WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
						WorkspaceDir:     "/workspace",
						Proxy: &SandboxProxy{
							Secrets: []string{"gh_token"},
							Rules: []SandboxProxyRule{
								{Name: "github-api", MatchHosts: []string{"api.github.com"}},
							},
						},
					},
					Telegram: Telegram{
						TokenEnv:       "TEST_TELEGRAM_TOKEN",
						AllowedUserIDs: []int64{123456789},
					},
				},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*SandboxProxy)
		wantErr string
	}{
		{
			name: "invalid env name",
			mutate: func(proxy *SandboxProxy) {
				proxy.Env = []SandboxProxyEnv{
					{Name: "GH-TOKEN", Secret: "gh_token", Rules: []string{"github-api"}},
				}
			},
			wantErr: "env[0].name",
		},
		{
			name: "duplicate env name",
			mutate: func(proxy *SandboxProxy) {
				proxy.Env = []SandboxProxyEnv{
					{Name: "GH_TOKEN", Secret: "gh_token", Rules: []string{"github-api"}},
					{Name: "GH_TOKEN", Secret: "gh_token", Rules: []string{"github-api"}},
				}
			},
			wantErr: `duplicates "GH_TOKEN"`,
		},
		{
			name: "unknown secret alias",
			mutate: func(proxy *SandboxProxy) {
				proxy.Env = []SandboxProxyEnv{
					{Name: "GH_TOKEN", Secret: "other_token", Rules: []string{"github-api"}},
				}
			},
			wantErr: `env[0].secret "other_token" is not defined in proxy.secrets`,
		},
		{
			name: "missing rule reference",
			mutate: func(proxy *SandboxProxy) {
				proxy.Env = []SandboxProxyEnv{
					{Name: "GH_TOKEN", Secret: "gh_token", Rules: []string{"missing-rule"}},
				}
			},
			wantErr: `does not match any proxy rule name`,
		},
		{
			name: "duplicate referenced rule name",
			mutate: func(proxy *SandboxProxy) {
				proxy.Rules = append(proxy.Rules, SandboxProxyRule{
					Name:       "github-api",
					MatchHosts: []string{"uploads.github.com"},
				})
				proxy.Env = []SandboxProxyEnv{
					{Name: "GH_TOKEN", Secret: "gh_token", Rules: []string{"github-api"}},
				}
			},
			wantErr: `matches multiple proxy rules`,
		},
		{
			name: "invalid replacement scope",
			mutate: func(proxy *SandboxProxy) {
				proxy.Env = []SandboxProxyEnv{
					{
						Name:   "GH_TOKEN",
						Secret: "gh_token",
						Rules:  []string{"github-api"},
						In:     []string{"cookie"},
					},
				}
			},
			wantErr: `env[0].in[0] must be header, query, or path`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			proxy := cfg.Agents[0].Sandbox.Proxy
			tc.mutate(proxy)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected validation error")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestResolveAgentRuntimesRequiresSandboxProxySecretEnv(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "proxy-agent",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-proxy-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"gh_token"},
						Rules: []SandboxProxyRule{
							{
								Name:       "github-api",
								MatchHosts: []string{"api.github.com"},
							},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if _, err := cfg.ResolveAgentRuntimes(); err == nil {
		t.Fatalf("expected runtime resolution error when proxy secret env var is missing")
	}
}

func TestResolveAgentRuntimesDerivesProxySecretEnvNameFromAlias(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")
	t.Setenv("GH_TOKEN", "ghp_derived")

	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "proxy-agent",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-proxy-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						ContainerProxyHost: "10.0.2.2",
						Secrets:            []string{"gh_token"},
						Rules: []SandboxProxyRule{
							{Name: "github-api", MatchHosts: []string{"api.github.com"}},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	runtimes, err := cfg.ResolveAgentRuntimes()
	if err != nil {
		t.Fatalf("resolve runtimes: %v", err)
	}
	if got := runtimes[0].SandboxProxy.SecretValues["gh_token"]; got != "ghp_derived" {
		t.Fatalf("unexpected derived proxy secret value: %q", got)
	}
}

func TestResolveAgentRuntimesUsesDefaultProxyContainerHostOverrideEnv(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")
	t.Setenv("GH_TOKEN", "ghp_derived")
	t.Setenv("Q15_SANDBOX_PROXY_CONTAINER_HOST", "10.0.2.2")

	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "proxy-agent",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-proxy-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"gh_token"},
						Rules: []SandboxProxyRule{
							{Name: "github-api", MatchHosts: []string{"api.github.com"}},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	runtimes, err := cfg.ResolveAgentRuntimes()
	if err != nil {
		t.Fatalf("resolve runtimes: %v", err)
	}
	if got := runtimes[0].SandboxProxy.ContainerProxyHost; got != "10.0.2.2" {
		t.Fatalf("unexpected proxy container host override: %q", got)
	}
}

func TestResolveAgentRuntimesIsolatesProxyEnvPlaceholdersPerAgent(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")
	t.Setenv("JARED_GH_TOKEN", "ghp_jared")
	t.Setenv("DINESH_GH_TOKEN", "ghp_dinesh")

	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "jared",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-jared",
					WorkspaceHostDir: "/tmp/q15-workspaces/jared",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"jared_gh_token"},
						Rules: []SandboxProxyRule{
							{Name: "github-api", MatchHosts: []string{"api.github.com"}},
						},
						Env: []SandboxProxyEnv{
							{
								Name:   "GH_TOKEN",
								Secret: "jared_gh_token",
								Rules:  []string{"github-api"},
							},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
			{
				Name:   "dinesh",
				Models: []string{"moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-dinesh",
					WorkspaceHostDir: "/tmp/q15-workspaces/dinesh",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"dinesh_gh_token"},
						Rules: []SandboxProxyRule{
							{Name: "github-api", MatchHosts: []string{"api.github.com"}},
						},
						Env: []SandboxProxyEnv{
							{
								Name:   "GH_TOKEN",
								Secret: "dinesh_gh_token",
								Rules:  []string{"github-api"},
							},
						},
					},
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	runtimes, err := cfg.ResolveAgentRuntimes()
	if err != nil {
		t.Fatalf("resolve runtimes: %v", err)
	}
	if len(runtimes) != 2 {
		t.Fatalf("expected 2 runtimes, got %d", len(runtimes))
	}

	jaredProxy := runtimes[0].SandboxProxy
	dineshProxy := runtimes[1].SandboxProxy
	if got := jaredProxy.SecretValues["jared_gh_token"]; got != "ghp_jared" {
		t.Fatalf("unexpected Jared proxy secret value: %q", got)
	}
	if got := dineshProxy.SecretValues["dinesh_gh_token"]; got != "ghp_dinesh" {
		t.Fatalf("unexpected Dinesh proxy secret value: %q", got)
	}
	jaredPlaceholder := jaredProxy.EnvValues["GH_TOKEN"]
	dineshPlaceholder := dineshProxy.EnvValues["GH_TOKEN"]
	if jaredPlaceholder == "" || dineshPlaceholder == "" {
		t.Fatalf("expected generated GH_TOKEN placeholders for both agents")
	}
	if jaredPlaceholder == dineshPlaceholder {
		t.Fatalf("expected unique placeholders per agent, got %q", jaredPlaceholder)
	}
	if got := jaredProxy.Rules[0].ReplacePlaceholder[0].Placeholder; got != jaredPlaceholder {
		t.Fatalf("unexpected Jared replacement placeholder: %q", got)
	}
	if got := dineshProxy.Rules[0].ReplacePlaceholder[0].Placeholder; got != dineshPlaceholder {
		t.Fatalf("unexpected Dinesh replacement placeholder: %q", got)
	}
}

func TestValidateAllowsOpenAICodexProviderWithoutBaseURLOrKeyEnv(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name: "openai-sub",
				Type: "openai-codex",
			},
		},
		Agents: []Agent{
			{
				Name:   "codex-agent",
				Models: []string{"openai-sub/gpt-5-codex"},
				Sandbox: Sandbox{
					ContainerName:    "q15-codex-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/codex",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected validation success for openai-codex provider, got %v", err)
	}
}

func TestResolveAgentRuntimesSupportsOpenAICodexAndOpenAICompatibleFallbacks(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-moonshot")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			{
				Name: "openai-sub",
				Type: "openai-codex",
			},
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "mixed-fallbacks",
				Models: []string{"openai-sub/gpt-5-codex", "moonshot/kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-mixed-fallbacks",
					WorkspaceHostDir: "/tmp/q15-workspaces/mixed-fallbacks",
					WorkspaceDir:     "/workspace",
				},
				Telegram: Telegram{
					TokenEnv:       "TEST_TELEGRAM_TOKEN",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	runtimes, err := cfg.ResolveAgentRuntimes()
	if err != nil {
		t.Fatalf("resolve runtimes: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 runtime, got %d", len(runtimes))
	}
	if len(runtimes[0].Models) != 2 {
		t.Fatalf("expected 2 resolved model fallbacks, got %#v", runtimes[0].Models)
	}

	first := runtimes[0].Models[0]
	if first.ProviderType != "openai-codex" {
		t.Fatalf("first provider type = %q, want %q", first.ProviderType, "openai-codex")
	}
	if first.ProviderAPIKey != "" {
		t.Fatalf("first provider api key = %q, want empty", first.ProviderAPIKey)
	}

	second := runtimes[0].Models[1]
	if second.ProviderType != "openai-compatible" {
		t.Fatalf("second provider type = %q, want %q", second.ProviderType, "openai-compatible")
	}
	if second.ProviderAPIKey != "api-moonshot" {
		t.Fatalf("second provider api key = %q, want %q", second.ProviderAPIKey, "api-moonshot")
	}
}

func TestNormalizeProviderTypeSupportsOpenAICodexAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "hyphenated",
			input: "openai-codex",
			want:  "openai-codex",
		},
		{
			name:  "underscored",
			input: "openai_codex",
			want:  "openai-codex",
		},
		{
			name:  "whitespace and casing",
			input: "  OpenAI-Codex ",
			want:  "openai-codex",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeProviderType(tc.input); got != tc.want {
				t.Fatalf("normalizeProviderType(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
