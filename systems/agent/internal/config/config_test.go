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
