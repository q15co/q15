package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testModel(name, provider string, capabilities ...string) Model {
	return Model{
		Name:         name,
		Provider:     provider,
		Capabilities: capabilities,
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
		Telegram: Telegram{
			TokenEnv:       "TEST_TELEGRAM_TOKEN",
			AllowedUserIDs: []int64{123456789},
		},
	}
}

func TestLoadAgentRuntimeYAML(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: moonshot
    type: openai-compatible
    base_url: https://api.moonshot.ai/v1
    key_env: MOONSHOT_API_KEY

models:
  - name: kimi-k2.5
    provider: moonshot
  - name: kimi-k2
    provider: moonshot

agent:
  name: Q15
  models:
    - kimi-k2.5
    - kimi-k2
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids:
      - 123456789
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("LoadAgentRuntime() returned nil runtime")
	}
	if runtime.Name != "Q15" {
		t.Fatalf("Name = %q, want %q", runtime.Name, "Q15")
	}
	if len(runtime.Models) != 2 {
		t.Fatalf("Models len = %d, want 2", len(runtime.Models))
	}
	if runtime.Models[0].ProviderAPIKey != "api-123" {
		t.Fatalf("ProviderAPIKey = %q, want %q", runtime.Models[0].ProviderAPIKey, "api-123")
	}
	if runtime.WorkspaceLocalDir != "/workspace" {
		t.Fatalf("WorkspaceLocalDir = %q, want %q", runtime.WorkspaceLocalDir, "/workspace")
	}
	if runtime.MemoryLocalDir != "/memory" {
		t.Fatalf("MemoryLocalDir = %q, want %q", runtime.MemoryLocalDir, "/memory")
	}
	if runtime.MediaLocalDir != "/media" {
		t.Fatalf("MediaLocalDir = %q, want %q", runtime.MediaLocalDir, "/media")
	}
	if runtime.SkillsLocalDir != "/skills" {
		t.Fatalf("SkillsLocalDir = %q, want %q", runtime.SkillsLocalDir, "/skills")
	}
	if runtime.Execution.ServiceAddress != "q15-exec:50051" {
		t.Fatalf(
			"Execution.ServiceAddress = %q, want %q",
			runtime.Execution.ServiceAddress,
			"q15-exec:50051",
		)
	}
	if runtime.TelegramToken != "tg-123" {
		t.Fatalf("TelegramToken = %q, want %q", runtime.TelegramToken, "tg-123")
	}
	if got, want := runtime.TelegramAllowedUserIDs, []int64{123456789}; len(got) != len(want) ||
		got[0] != want[0] {
		t.Fatalf("TelegramAllowedUserIDs = %#v, want %#v", got, want)
	}
}

func TestLoadAgentRuntimeYAMLResolvesTelegramAllowedUserIDsFromEnvFile(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg-123")
	idsPath := filepath.Join(t.TempDir(), "allowed_user_ids")
	if err := os.WriteFile(idsPath, []byte("123456789\n987654321\n"), 0o644); err != nil {
		t.Fatalf("write allowed_user_ids file: %v", err)
	}
	t.Setenv("Q15_TELEGRAM_ALLOWED_USER_IDS_FILE", idsPath)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: moonshot
    type: openai-compatible
    base_url: https://api.moonshot.ai/v1
    key_env: MOONSHOT_API_KEY

models:
  - name: kimi-k2.5
    provider: moonshot

agent:
  name: Q15
  models:
    - kimi-k2.5
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids_env: Q15_TELEGRAM_ALLOWED_USER_IDS
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("LoadAgentRuntime() returned nil runtime")
	}
	want := []int64{123456789, 987654321}
	if len(runtime.TelegramAllowedUserIDs) != len(want) {
		t.Fatalf(
			"TelegramAllowedUserIDs len = %d, want %d",
			len(runtime.TelegramAllowedUserIDs),
			len(want),
		)
	}
	for i := range want {
		if runtime.TelegramAllowedUserIDs[i] != want[i] {
			t.Fatalf("TelegramAllowedUserIDs = %#v, want %#v", runtime.TelegramAllowedUserIDs, want)
		}
	}
}

func TestLoadAgentRuntimeEmptyConfigReturnsNilRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("# empty\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if runtime != nil {
		t.Fatalf("expected nil runtime, got %#v", runtime)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
agent:
  name: Q15
  unknown_runtime: true
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown_runtime") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestValidateRequiresProviderWhenAgentConfigured(t *testing.T) {
	cfg := Config{
		Agent: testAgent("missing-provider", "kimi-k2.5"),
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when providers are missing")
	}
	if !strings.Contains(err.Error(), "providers cannot be empty") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnknownAgentModel(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider(
				"moonshot",
				"https://api.moonshot.ai/v1",
				"MOONSHOT_API_KEY",
			),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot"),
		},
		Agent: testAgent("legacy", "missing-model"),
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown model")
	}
	if !strings.Contains(err.Error(), `model "missing-model" is not defined in models`) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateRejectsUnsupportedModelCapability(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICompatibleProvider(
				"moonshot",
				"https://api.moonshot.ai/v1",
				"MOONSHOT_API_KEY",
			),
		},
		Models: []Model{
			testModel("kimi-k2.5", "moonshot", "text", "video_input"),
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported capability")
	}
	if !strings.Contains(
		err.Error(),
		`models[0].capabilities: [1] "video_input" is not supported`,
	) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestResolveAgentRuntimeResolvesOpenAICodexProviderWithoutAPIKey(t *testing.T) {
	t.Setenv("TEST_TELEGRAM_TOKEN", "tg-123")

	cfg := Config{
		Providers: []Provider{
			testOpenAICodexProvider("openai"),
		},
		Models: []Model{
			testModel("gpt-5", "openai", "text", "tool_calling", "reasoning"),
		},
		Agent: testAgent("Codex", "gpt-5"),
	}

	runtime, err := cfg.ResolveAgentRuntime()
	if err != nil {
		t.Fatalf("ResolveAgentRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("ResolveAgentRuntime() returned nil runtime")
	}
	if got := runtime.Models[0].ProviderAPIKey; got != "" {
		t.Fatalf("ProviderAPIKey = %q, want empty string", got)
	}
}

func TestValidateRejectsBothTelegramAllowedUserIDSources(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			testOpenAICodexProvider("openai"),
		},
		Models: []Model{
			testModel("gpt-5", "openai", "text"),
		},
		Agent: &Agent{
			Name:   "Q15",
			Models: []string{"gpt-5"},
			Telegram: Telegram{
				TokenEnv:          "TEST_TELEGRAM_TOKEN",
				AllowedUserIDs:    []int64{123456789},
				AllowedUserIDsEnv: "Q15_TELEGRAM_ALLOWED_USER_IDS",
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for mixed allowed user id sources")
	}
	if !strings.Contains(err.Error(), "allowed_user_ids_env are mutually exclusive") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
