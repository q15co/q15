package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testModel(name, provider string, capabilities ...string) Model {
	return testModelWithProviderModel(name, provider, "", capabilities...)
}

func testModelWithProviderModel(
	name string,
	provider string,
	providerModel string,
	capabilities ...string,
) Model {
	return Model{
		Name:          name,
		Provider:      provider,
		ProviderModel: providerModel,
		Capabilities:  capabilities,
	}
}

func TestLoadAgentRuntimes_TOML(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[skills]
host_dir = "/tmp/q15-shared-skills"

[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[model]]
name = "kimi-k2.5"
provider = "moonshot"

[[model]]
name = "kimi-k2"
provider = "moonshot"

	[[agent]]
	name = "Jared"
	models = ["kimi-k2.5", "kimi-k2"]

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
	if rt.Models[0].Ref != "kimi-k2.5" || rt.Models[0].ProviderModel != "kimi-k2.5" {
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
	if !rt.Models[0].Capabilities.Text {
		t.Fatalf("unexpected default capabilities: %#v", rt.Models[0].Capabilities)
	}
	if rt.Models[0].Capabilities.ToolCalling ||
		rt.Models[0].Capabilities.ImageInput ||
		rt.Models[0].Capabilities.Reasoning {
		t.Fatalf("unexpected extra default capabilities: %#v", rt.Models[0].Capabilities)
	}
	if rt.Models[1].Ref != "kimi-k2" || rt.Models[1].ProviderModel != "kimi-k2" {
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
	if rt.SkillsHostDir != "/tmp/q15-shared-skills" {
		t.Fatalf("unexpected skills host dir: %q", rt.SkillsHostDir)
	}
	if rt.SkillsDir != "/skills" {
		t.Fatalf("unexpected skills dir: %q", rt.SkillsDir)
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

func TestValidateRejectsRelativeSkillsHostDir(t *testing.T) {
	cfg := Config{
		Skills: Skills{
			HostDir: "relative/skills",
		},
	}

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "skills: host_dir must be an absolute path") {
		t.Fatalf("Validate() error = %v, want skills host dir validation", err)
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

[[model]]
name = "kimi-k2.5"
provider = "moonshot"

[[agent]]
name = "Jared"
models = ["kimi-k2.5"]
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

func TestLoadAgentRuntimes_TOML_ExecutionService(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[model]]
name = "kimi-k2.5"
provider = "moonshot"

[[agent]]
name = "Jared"
models = ["kimi-k2.5"]

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.execution]
service_address = "127.0.0.1:50051"

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
	if runtimes[0].Execution == nil {
		t.Fatalf("expected execution runtime to be resolved")
	}
	if got := runtimes[0].Execution.ServiceAddress; got != "127.0.0.1:50051" {
		t.Fatalf("execution service address = %q, want %q", got, "127.0.0.1:50051")
	}
}

func TestLoadAgentRuntimes_TOML_ExplicitModelMetadata(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[model]]
name = "vision-primary"
provider = "moonshot"
provider_model = "kimi-k2.5"
capabilities = ["text", "image_input", "reasoning"]

[[agent]]
name = "Jared"
models = ["vision-primary"]

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
	if len(runtimes) != 1 || len(runtimes[0].Models) != 1 {
		t.Fatalf("unexpected runtimes: %#v", runtimes)
	}

	model := runtimes[0].Models[0]
	if model.Ref != "vision-primary" {
		t.Fatalf("model ref = %q, want %q", model.Ref, "vision-primary")
	}
	if model.ProviderModel != "kimi-k2.5" {
		t.Fatalf("provider model = %q, want %q", model.ProviderModel, "kimi-k2.5")
	}
	if !model.Capabilities.Text || !model.Capabilities.ImageInput || !model.Capabilities.Reasoning {
		t.Fatalf("unexpected capabilities: %#v", model.Capabilities)
	}
	if model.Capabilities.ToolCalling {
		t.Fatalf("unexpected tool calling capability: %#v", model.Capabilities)
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

func TestValidateRejectsEmptyExecutionServiceAddress(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			{
				Name:     "kimi-k2.5",
				Provider: "moonshot",
			},
		},
		Agents: []Agent{
			{
				Name:   "Jared",
				Models: []string{"kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-jared",
					WorkspaceHostDir: "/tmp/q15-workspaces/jared",
					WorkspaceDir:     "/workspace",
				},
				Execution: &Execution{},
				Telegram: Telegram{
					Token:          "tg-123",
					AllowedUserIDs: []int64{123456789},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "agent[0].execution: service_address is required") {
		t.Fatalf("Validate() error = %v, want execution service address validation", err)
	}
}

func TestValidateRequiresProviderWhenAgentConfigured(t *testing.T) {
	cfg := Config{
		Agents: []Agent{
			{
				Name:   "missing-provider",
				Models: []string{"kimi-k2.5"},
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
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
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

func TestValidateRejectsLegacyAgentModelRefs(t *testing.T) {
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
				Name:   "legacy",
				Models: []string{"moonshot/kimi-k2.5"},
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

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for legacy model ref")
	}
	if !strings.Contains(err.Error(), `legacy "provider/model" format`) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnknownAgentModel(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agents: []Agent{
			{
				Name:   "legacy",
				Models: []string{"missing-model"},
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

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for unknown model")
	}
	if !strings.Contains(err.Error(), `model "missing-model" is not defined in models`) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsDuplicateModelNames(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
			testModel("kimi-k2.5", "moonshot"),
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for duplicate model names")
	}
	if !strings.Contains(err.Error(), `duplicate model name "kimi-k2.5"`) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsModelNamesWithSlash(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			testModel("moonshot/kimi-k2.5", "moonshot"),
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for slashy model names")
	}
	if !strings.Contains(err.Error(), "model[0].name must not contain /") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnsupportedModelCapability(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot", "text", "video_input"),
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for unsupported capability")
	}
	if !strings.Contains(err.Error(), `model[0].capabilities: [1] "video_input" is not supported`) {
		t.Fatalf("unexpected validation error: %v", err)
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
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agents: []Agent{
			{
				Name:   "no-allowlist",
				Models: []string{"kimi-k2.5"},
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
		Models: []Model{
			testModel("glm-4.5", "zai"),
		},
		Agents: []Agent{
			{
				Name:   "zai-agent",
				Models: []string{"glm-4.5"},
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
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
			testModel("glm-5", "zai"),
		},
		Agents: []Agent{
			{
				Name:   "mixed-fallbacks",
				Models: []string{"kimi-k2.5", "glm-5"},
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
		runtimes[0].Models[0].ProviderModel != "kimi-k2.5" {
		t.Fatalf("unexpected first fallback: %#v", runtimes[0].Models[0])
	}
	if runtimes[0].Models[1].ProviderName != "zai" ||
		runtimes[0].Models[1].ProviderModel != "glm-5" {
		t.Fatalf("unexpected second fallback: %#v", runtimes[0].Models[1])
	}
}

func TestLoadAgentRuntimesRejectsDeprecatedSandboxProxyConfig(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[model]]
name = "kimi-k2.5"
provider = "moonshot"

[[agent]]
name = "Jared"
models = ["kimi-k2.5"]

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/tmp/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.sandbox.proxy]
secrets = ["gh_token"]

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadAgentRuntimes(path); err == nil {
		t.Fatalf("expected deprecated sandbox proxy config to be rejected")
	} else if !strings.Contains(err.Error(), "has moved to q15-proxy-service config") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsDeprecatedSandboxProxyConfig(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:    "moonshot",
				Type:    "openai-compatible",
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
			},
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agents: []Agent{
			{
				Name:   "proxy-agent",
				Models: []string{"kimi-k2.5"},
				Sandbox: Sandbox{
					ContainerName:    "q15-proxy-agent",
					WorkspaceHostDir: "/tmp/q15-workspaces/proxy-agent",
					WorkspaceDir:     "/workspace",
					Proxy: &SandboxProxy{
						Secrets: []string{"gh_token"},
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
		t.Fatalf("expected validation error for deprecated sandbox proxy config")
	} else if !strings.Contains(err.Error(), "has moved to q15-proxy-service config") {
		t.Fatalf("unexpected validation error: %v", err)
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
		Models: []Model{
			testModel("gpt-5-codex", "openai-sub"),
		},
		Agents: []Agent{
			{
				Name:   "codex-agent",
				Models: []string{"gpt-5-codex"},
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
		Models: []Model{
			testModel("gpt-5-codex", "openai-sub"),
			testModel("kimi-k2.5", "moonshot"),
		},
		Agents: []Agent{
			{
				Name:   "mixed-fallbacks",
				Models: []string{"gpt-5-codex", "kimi-k2.5"},
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
