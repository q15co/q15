package config

import (
	"os"
	"path/filepath"
	"testing"
)

func testProvider(name, typ, baseURL, keyEnv string) Provider {
	return Provider{
		Name:    name,
		Type:    typ,
		BaseURL: baseURL,
		KeyEnv:  keyEnv,
	}
}

func testAgent(name, model string) *Agent {
	return &Agent{
		Name:  name,
		Model: model,
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
agent:
  name: Q15
  model: kimi-k2.7-code
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [123456789]
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
	if runtime.CurrentModelRef != "kimi-k2.7-code" {
		t.Fatalf("CurrentModelRef = %q, want kimi-k2.7-code", runtime.CurrentModelRef)
	}
	if runtime.CurrentCognitionModelRef != "kimi-k2.7-code" {
		t.Fatalf(
			"CurrentCognitionModelRef = %q, want kimi-k2.7-code",
			runtime.CurrentCognitionModelRef,
		)
	}
	if runtime.Name != "Q15" {
		t.Fatalf("Name = %q, want Q15", runtime.Name)
	}
	if runtime.TelegramToken != "tg-123" {
		t.Fatalf("TelegramToken = %q, want tg-123", runtime.TelegramToken)
	}
}

func TestLoadAgentRuntimeYAMLResolvesCognitionModel(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "api-123")
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg-123")

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: moonshot
    type: openai-compatible
    base_url: https://api.moonshot.ai/v1
    key_env: MOONSHOT_API_KEY
agent:
  name: Q15
  model: kimi-k2.7-code
  cognition_model: nemotron-3-ultra
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [123456789]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if runtime.CurrentModelRef != "kimi-k2.7-code" {
		t.Fatalf("CurrentModelRef = %q, want kimi-k2.7-code", runtime.CurrentModelRef)
	}
	if runtime.CurrentCognitionModelRef != "nemotron-3-ultra" {
		t.Fatalf(
			"CurrentCognitionModelRef = %q, want nemotron-3-ultra",
			runtime.CurrentCognitionModelRef,
		)
	}
}

func TestLoadAgentRuntimeEmptyConfigReturnsNilRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(``), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	runtime, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if runtime != nil {
		t.Fatal("expected nil runtime for empty config")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  unknown_field: true
  telegram:
    token_env: T
    allowed_user_ids: [1]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateRequiresProviderWhenAgentConfigured(t *testing.T) {
	cfg := Config{
		Agent: testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when no providers with agent")
	}
}

func TestValidateRequiresAgentModel(t *testing.T) {
	cfg := Config{
		Providers: []Provider{testProvider("p", "ollama", "", "")},
		Agent: &Agent{
			Name: "a",
			Telegram: Telegram{
				TokenEnv:       "T",
				AllowedUserIDs: []int64{1},
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when agent.model is missing")
	}
}

func TestValidateRejectsUnsupportedProviderType(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{Name: "p", Type: "imaginary"}},
		Agent:     testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unsupported provider type")
	}
}

func TestValidateRequiresKeyEnvForOpenAICompatible(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{Name: "p", Type: "openai-compatible", BaseURL: "https://x"}},
		Agent:     testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing key_env")
	}
}

func TestValidateRequiresBaseURLForOpenAICompatible(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{Name: "p", Type: "openai-compatible", KeyEnv: "K"}},
		Agent:     testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing base_url")
	}
}

func TestValidateRequiresKeyEnvForOllamaCloud(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{Name: "p", Type: "ollama", BaseURL: "https://ollama.com"}},
		Agent:     testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing key_env on Ollama Cloud")
	}
}

func TestValidateAcceptsDiscoveryGlobs(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{
			Name:    "p",
			Type:    "ollama",
			BaseURL: "http://localhost:11434",
			Discovery: ProviderDiscovery{
				Include: []string{"kimi-*"},
				Exclude: []string{"*-embed"},
			},
		}},
		Agent: testAgent("a", "m"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsBadDiscoveryGlob(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{
			Name:    "p",
			Type:    "ollama",
			BaseURL: "http://localhost:11434",
			Discovery: ProviderDiscovery{
				Include: []string{"["},
			},
		}},
		Agent: testAgent("a", "m"),
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid glob")
	}
}

func TestValidateRequiresTelegramToken(t *testing.T) {
	cfg := Config{
		Providers: []Provider{testProvider("p", "ollama", "", "")},
		Agent: &Agent{
			Name:  "a",
			Model: "m",
			Telegram: Telegram{
				AllowedUserIDs: []int64{1},
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing telegram token")
	}
}

func TestValidateRequiresTelegramAllowedUsers(t *testing.T) {
	cfg := Config{
		Providers: []Provider{testProvider("p", "ollama", "", "")},
		Agent: &Agent{
			Name:  "a",
			Model: "m",
			Telegram: Telegram{
				TokenEnv: "T",
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing allowed_user_ids")
	}
}

func TestValidateRejectsBothTelegramAllowedUserIDSources(t *testing.T) {
	cfg := Config{
		Providers: []Provider{testProvider("p", "ollama", "", "")},
		Agent: &Agent{
			Name:  "a",
			Model: "m",
			Telegram: Telegram{
				TokenEnv:          "T",
				AllowedUserIDs:    []int64{1},
				AllowedUserIDsEnv: "U",
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for both allowed_user_ids sources")
	}
}

func TestResolveAgentRuntimeResolvesLocalOllamaProviderWithoutAPIKey(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "t")
	cfg := Config{
		Providers: []Provider{testProvider("local", "ollama", "http://localhost:11434", "")},
		Agent: &Agent{
			Name:  "a",
			Model: "m",
			Telegram: Telegram{
				TokenEnv:       "Q15_TELEGRAM_TOKEN",
				AllowedUserIDs: []int64{1},
			},
		},
	}
	rt, err := cfg.ResolveAgentRuntime()
	if err != nil {
		t.Fatalf("ResolveAgentRuntime() error = %v", err)
	}
	if rt.CurrentModelRef != "m" {
		t.Fatalf("CurrentModelRef = %q, want m", rt.CurrentModelRef)
	}
}

func TestResolveAgentRuntimeResolvesOpenAICodexProviderWithoutAPIKey(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "t")
	cfg := Config{
		Providers: []Provider{{Name: "codex", Type: "openai-codex"}},
		Agent: &Agent{
			Name:  "a",
			Model: "m",
			Telegram: Telegram{
				TokenEnv:       "Q15_TELEGRAM_TOKEN",
				AllowedUserIDs: []int64{1},
			},
		},
	}
	rt, err := cfg.ResolveAgentRuntime()
	if err != nil {
		t.Fatalf("ResolveAgentRuntime() error = %v", err)
	}
	if rt.CurrentModelRef != "m" {
		t.Fatalf("CurrentModelRef = %q, want m", rt.CurrentModelRef)
	}
}

func TestLoadAgentRuntimeYAMLResolvesTelegramAllowedUserIDsFromEnvFile(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
	dir := t.TempDir()
	idsFile := filepath.Join(dir, "ids")
	if err := os.WriteFile(idsFile, []byte("111, 222"), 0o644); err != nil {
		t.Fatalf("write ids file: %v", err)
	}
	t.Setenv("Q15_TELEGRAM_ALLOWED_USER_IDS_FILE", idsFile)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids_env: Q15_TELEGRAM_ALLOWED_USER_IDS
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rt, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if len(rt.TelegramAllowedUserIDs) != 2 || rt.TelegramAllowedUserIDs[0] != 111 ||
		rt.TelegramAllowedUserIDs[1] != 222 {
		t.Fatalf("TelegramAllowedUserIDs = %v, want [111 222]", rt.TelegramAllowedUserIDs)
	}
}

func TestLoadAgentRuntimeYAMLResolvesBraveAPIKeyFromConfiguredEnvFile(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
	dir := t.TempDir()
	braveFile := filepath.Join(dir, "brave")
	if err := os.WriteFile(braveFile, []byte("brave-key"), 0o644); err != nil {
		t.Fatalf("write brave file: %v", err)
	}
	t.Setenv("BRAVE_API_KEY_FILE", braveFile)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  tools:
    web_search:
      brave_api_key_env: BRAVE_API_KEY
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [1]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rt, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if rt.Tools.WebSearch.BraveAPIKey != "brave-key" {
		t.Fatalf("BraveAPIKey = %q, want brave-key", rt.Tools.WebSearch.BraveAPIKey)
	}
}

func TestLoadAgentRuntimeYAMLRequiresConfiguredBraveAPIKeyEnv(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  tools:
    web_search:
      brave_api_key_env: MISSING_BRAVE
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [1]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadAgentRuntime(path); err == nil {
		t.Fatal("expected error for missing BRAVE_API_KEY")
	}
}

func TestLoadAgentRuntimeYAMLResolvesEmbeddingsFromConfiguredEnvFiles(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
	t.Setenv("Q15_QDRANT_URL", "http://qdrant:6333")
	t.Setenv("Q15_GEMINI_API_KEY", "gemini-key")

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  tools:
    embeddings:
      qdrant_url_env: Q15_QDRANT_URL
      gemini_api_key_env: Q15_GEMINI_API_KEY
      model: gemini-embedding-2
      dimensions: 768
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [1]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rt, err := LoadAgentRuntime(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntime() error = %v", err)
	}
	if !rt.Tools.Embeddings.Enabled {
		t.Fatal("Embeddings not enabled")
	}
	if rt.Tools.Embeddings.Model != "gemini-embedding-2" {
		t.Fatalf("Embeddings.Model = %q, want gemini-embedding-2", rt.Tools.Embeddings.Model)
	}
}

func TestLoadAgentRuntimeYAMLRequiresConfiguredEmbeddingsEnv(t *testing.T) {
	t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  model: m
  tools:
    embeddings:
      qdrant_url_env: MISSING_QDRANT
      gemini_api_key_env: Q15_GEMINI_API_KEY
      model: gemini-embedding-2
      dimensions: 768
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [1]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadAgentRuntime(path); err == nil {
		t.Fatal("expected error for missing QDRANT URL")
	}
}
