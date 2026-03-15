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

func testOpenAICompatibleProvider(name, baseURL, keyEnv string) Provider {
	return Provider{
		Name:    name,
		Type:    "openai-compatible",
		BaseURL: baseURL,
		KeyEnv:  keyEnv,
	}
}

func testOpenAICodexProvider(name string) Provider {
	return Provider{
		Name: name,
		Type: "openai-codex",
	}
}

func testAgent(name string, modelRefs ...string) *Agent {
	return &Agent{
		Name:   name,
		Models: append([]string(nil), modelRefs...),
		Workspace: Workspace{
			LocalDir: "/tmp/q15-workspaces/" + strings.ToLower(name),
		},
		Execution: &Execution{
			ServiceAddress: "exec-service:50051",
		},
		Telegram: Telegram{
			TokenEnv:       "TEST_TELEGRAM_TOKEN",
			AllowedUserIDs: []int64{123456789},
		},
	}
}

func TestLoadAgentRuntimeTOML(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("JARED_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[skills]
local_dir = "/tmp/q15-shared-skills"

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

[agent]
name = "Jared"
models = ["kimi-k2.5", "kimi-k2"]

[agent.workspace]
local_dir = "/tmp/q15-workspaces/jared"

[agent.execution]
service_address = "exec-service:50051"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if runtime == nil {
		t.Fatal("LoadAgentRuntime() returned nil runtime")
	}

	if runtime.Name != "Jared" {
		t.Fatalf("unexpected agent name: %q", runtime.Name)
	}
	if len(runtime.Models) != 2 {
		t.Fatalf("unexpected models: %#v", runtime.Models)
	}
	if runtime.Models[0].Ref != "kimi-k2.5" || runtime.Models[0].ProviderModel != "kimi-k2.5" {
		t.Fatalf("unexpected first model runtime: %#v", runtime.Models[0])
	}
	if runtime.Models[0].ProviderType != "openai-compatible" {
		t.Fatalf("unexpected provider type: %q", runtime.Models[0].ProviderType)
	}
	if runtime.Models[0].ProviderBaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("unexpected provider base url: %q", runtime.Models[0].ProviderBaseURL)
	}
	if runtime.Models[0].ProviderAPIKey != "api-123" {
		t.Fatalf("unexpected provider api key: %q", runtime.Models[0].ProviderAPIKey)
	}
	if !runtime.Models[0].Capabilities.Text {
		t.Fatalf("unexpected default capabilities: %#v", runtime.Models[0].Capabilities)
	}
	if runtime.Models[1].Ref != "kimi-k2" {
		t.Fatalf("unexpected second model runtime: %#v", runtime.Models[1])
	}
	if runtime.WorkspaceLocalDir != "/tmp/q15-workspaces/jared" {
		t.Fatalf("unexpected workspace local dir: %q", runtime.WorkspaceLocalDir)
	}
	if runtime.MemoryLocalDir != "/tmp/q15-workspaces/jared/.q15-memory" {
		t.Fatalf("unexpected memory local dir: %q", runtime.MemoryLocalDir)
	}
	if runtime.SkillsLocalDir != "/tmp/q15-shared-skills" {
		t.Fatalf("unexpected skills local dir: %q", runtime.SkillsLocalDir)
	}
	if runtime.Execution.ServiceAddress != "exec-service:50051" {
		t.Fatalf("unexpected execution service address: %q", runtime.Execution.ServiceAddress)
	}
	if runtime.MemoryRecentTurns != 6 {
		t.Fatalf("unexpected default memory recent turns: %d", runtime.MemoryRecentTurns)
	}
	if runtime.TelegramToken != "tg-123" {
		t.Fatalf("unexpected telegram token: %q", runtime.TelegramToken)
	}
	if len(runtime.TelegramAllowedUserIDs) != 1 || runtime.TelegramAllowedUserIDs[0] != 123456789 {
		t.Fatalf("unexpected allowed telegram user ids: %#v", runtime.TelegramAllowedUserIDs)
	}
}

func TestLoadAgentRuntimeEmptyConfigReturnsNilRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# starter config\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if runtime != nil {
		t.Fatalf("expected nil runtime, got %#v", runtime)
	}
}

func TestValidateRejectsRelativeSkillsLocalDir(t *testing.T) {
	cfg := Config{
		Skills: Skills{
			LocalDir: "relative/skills",
		},
	}

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "skills: local_dir must be an absolute path") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadRejectsUnknownSkillsField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[skills]
unknown_dir = "/tmp/q15-shared-skills"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown_dir") {
		t.Fatalf("Load() error = %v, want unknown unknown_dir error", err)
	}
}

func TestValidateRejectsEmptyExecutionServiceAddress(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("Jared", "kimi-k2.5"),
	}
	cfg.Agent.Execution = &Execution{}

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "agent.execution: service_address is required") {
		t.Fatalf("Validate() error = %v, want execution service address validation", err)
	}
}

func TestValidateRequiresProviderWhenAgentConfigured(t *testing.T) {
	cfg := Config{
		Agent: testAgent("missing-provider", "kimi-k2.5"),
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error when providers are missing")
	}
	if !strings.Contains(err.Error(), "provider cannot be empty") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRequiresExecutionConfig(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("Jared", "kimi-k2.5"),
	}
	cfg.Agent.Execution = nil

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "agent.execution: is required") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadRejectsUnknownAgentSection(t *testing.T) {
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

[agent]
name = "Jared"
models = ["kimi-k2.5"]

[agent.workspace]
local_dir = "/tmp/q15-workspaces/jared"

[agent.unknown_runtime]
local_dir = "/tmp/q15-workspaces/jared"

[agent.execution]
service_address = "exec-service:50051"

[agent.telegram]
token = "tg-123"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown_runtime") {
		t.Fatalf("Load() error = %v, want unknown unknown_runtime section error", err)
	}
}

func TestValidateRequiresAgentModels(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("legacy"),
	}
	cfg.Agent.Models = nil

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when agent.models is missing")
	}
}

func TestValidateRejectsLegacyAgentModelRefs(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Agent: testAgent("legacy", "moonshot/kimi-k2.5"),
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error for legacy model ref")
	}
	if !strings.Contains(err.Error(), `unsupported "provider/model" format`) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnknownAgentModel(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("legacy", "missing-model"),
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
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
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
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
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
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
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
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("no-allowlist", "kimi-k2.5"),
	}
	cfg.Agent.Telegram.AllowedUserIDs = nil

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
		Agent: testAgent("zai-agent", "glm-4.5"),
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error when provider.base_url is missing")
	}
}

func TestResolveAgentRuntimeSupportsMixedProviderFallbacks(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-moonshot")
	t.Setenv("ZAI_API_KEY", "api-zai")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
			testOpenAICompatibleProvider("zai", "https://example.com/openai/v1", "ZAI_API_KEY"),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
			testModel("glm-5", "zai"),
		},
		Agent: testAgent("mixed-fallbacks", "kimi-k2.5", "glm-5"),
	}

	runtime, err := cfg.ResolveAgentRuntime()
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if runtime == nil {
		t.Fatal("ResolveAgentRuntime() returned nil runtime")
	}
	if len(runtime.Models) != 2 {
		t.Fatalf("expected 2 resolved model fallbacks, got %#v", runtime.Models)
	}
	if runtime.Models[0].ProviderName != "moonshot" ||
		runtime.Models[0].ProviderModel != "kimi-k2.5" {
		t.Fatalf("unexpected first fallback: %#v", runtime.Models[0])
	}
	if runtime.Models[1].ProviderName != "zai" ||
		runtime.Models[1].ProviderModel != "glm-5" {
		t.Fatalf("unexpected second fallback: %#v", runtime.Models[1])
	}
}

func TestLoadAgentRuntimeTOMLExplicitModelMetadata(t *testing.T) {
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

[agent]
name = "Jared"
models = ["vision-primary"]

[agent.workspace]
local_dir = "/tmp/q15-workspaces/jared"

[agent.execution]
service_address = "exec-service:50051"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	if runtime == nil || len(runtime.Models) != 1 {
		t.Fatalf("unexpected runtime: %#v", runtime)
	}

	model := runtime.Models[0]
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

func TestValidateAllowsOpenAICodexProviderWithoutBaseURLOrKeyEnv(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICodexProvider("openai-sub"),
		},
		Models: []Model{
			testModel("gpt-5-codex", "openai-sub"),
		},
		Agent: testAgent("codex-agent", "gpt-5-codex"),
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected validation success for openai-codex provider, got %v", err)
	}
}

func TestResolveAgentRuntimeSupportsOpenAICodexAndOpenAICompatibleFallbacks(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-moonshot")
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			testOpenAICodexProvider("openai-sub"),
			testOpenAICompatibleProvider("moonshot", "https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"),
		},
		Models: []Model{
			testModel("gpt-5-codex", "openai-sub"),
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("mixed-fallbacks", "gpt-5-codex", "kimi-k2.5"),
	}

	runtime, err := cfg.ResolveAgentRuntime()
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if runtime == nil {
		t.Fatal("ResolveAgentRuntime() returned nil runtime")
	}
	if len(runtime.Models) != 2 {
		t.Fatalf("expected 2 resolved model fallbacks, got %#v", runtime.Models)
	}

	first := runtime.Models[0]
	if first.ProviderType != "openai-codex" {
		t.Fatalf("first provider type = %q, want %q", first.ProviderType, "openai-codex")
	}
	if first.ProviderAPIKey != "" {
		t.Fatalf("first provider api key = %q, want empty", first.ProviderAPIKey)
	}

	second := runtime.Models[1]
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
