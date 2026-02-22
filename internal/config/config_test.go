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
model = "moonshot/kimi-k2.5"

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
	if rt.ProviderType != "openai-compatible" {
		t.Fatalf("unexpected provider type: %q", rt.ProviderType)
	}
	if rt.ProviderBaseURL != "https://api.moonshot.ai/v1" {
		t.Fatalf("unexpected provider base url: %q", rt.ProviderBaseURL)
	}
	if rt.ProviderAPIKey != "api-123" {
		t.Fatalf("unexpected provider api key: %q", rt.ProviderAPIKey)
	}
	if len(rt.Models) != 1 || rt.Models[0] != "kimi-k2.5" {
		t.Fatalf("unexpected models: %#v", rt.Models)
	}
	if rt.TelegramToken != "tg-123" {
		t.Fatalf("unexpected telegram token: %q", rt.TelegramToken)
	}
	if len(rt.TelegramAllowedUserIDs) != 1 || rt.TelegramAllowedUserIDs[0] != 123456789 {
		t.Fatalf("unexpected allowed telegram user ids: %#v", rt.TelegramAllowedUserIDs)
	}
}

func TestValidateRejectsLegacyAgentModelsList(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:   "moonshot",
				Type:   "openai-compatible",
				KeyEnv: "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:   "legacy",
				Models: []string{"moonshot/kimi-k2.5"},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for legacy agent.models")
	}
}

func TestValidateRequiresTelegramAllowedUserIDs(t *testing.T) {
	cfg := Config{
		Providers: []Provider{
			{
				Name:   "moonshot",
				Type:   "openai-compatible",
				KeyEnv: "MOONSHOT_API_KEY",
			},
		},
		Agents: []Agent{
			{
				Name:  "no-allowlist",
				Model: "moonshot/kimi-k2.5",
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
