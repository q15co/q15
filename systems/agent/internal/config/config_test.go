package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentRuntimes_TOML(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "q15.toml")
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
	from_image = "docker.io/library/debian:bookworm-slim"
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
	if rt.SandboxFromImage != "docker.io/library/debian:bookworm-slim" {
		t.Fatalf("unexpected sandbox base image: %q", rt.SandboxFromImage)
	}
	if rt.WorkspaceHostDir != "/tmp/q15-workspaces/jared" {
		t.Fatalf("unexpected workspace host dir: %q", rt.WorkspaceHostDir)
	}
	if rt.WorkspaceDir != "/workspace" {
		t.Fatalf("unexpected workspace dir: %q", rt.WorkspaceDir)
	}
	if rt.TelegramToken != "tg-123" {
		t.Fatalf("unexpected telegram token: %q", rt.TelegramToken)
	}
	if len(rt.TelegramAllowedUserIDs) != 1 || rt.TelegramAllowedUserIDs[0] != 123456789 {
		t.Fatalf("unexpected allowed telegram user ids: %#v", rt.TelegramAllowedUserIDs)
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
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

	path := filepath.Join(t.TempDir(), "q15.toml")
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
from_image = "docker.io/library/debian:bookworm-slim"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"
network = "enabled"

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

func TestValidateRejectsSandboxProxyEnabledWithNetworkDisabled(t *testing.T) {
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Network:          "disabled",
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

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when sandbox proxy is enabled with network disabled")
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Network:          "enabled",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Network:          "enabled",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Network:          "enabled",
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
					FromImage:        "docker.io/library/debian:bookworm-slim",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Network:          "enabled",
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
