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

func TestLoadAgentRuntimesTOML(t *testing.T) {
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

[[agent]]
name = "Jared"
models = ["kimi-k2.5"]

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

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntimes() error = %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("len(runtimes) = %d, want 1", len(runtimes))
	}

	rt := runtimes[0]
	if rt.Name != "Jared" {
		t.Fatalf("rt.Name = %q, want Jared", rt.Name)
	}
	if rt.WorkspaceLocalDir != "/tmp/q15-workspaces/jared" {
		t.Fatalf("rt.WorkspaceLocalDir = %q", rt.WorkspaceLocalDir)
	}
	if rt.MemoryLocalDir != "/tmp/q15-workspaces/jared/.q15-memory" {
		t.Fatalf("rt.MemoryLocalDir = %q", rt.MemoryLocalDir)
	}
	if rt.SkillsLocalDir != "/tmp/q15-shared-skills" {
		t.Fatalf("rt.SkillsLocalDir = %q", rt.SkillsLocalDir)
	}
	if rt.Execution.ServiceAddress != "exec-service:50051" {
		t.Fatalf("rt.Execution.ServiceAddress = %q", rt.Execution.ServiceAddress)
	}
	if rt.MemoryRecentTurns != 6 {
		t.Fatalf("rt.MemoryRecentTurns = %d, want 6", rt.MemoryRecentTurns)
	}
	if rt.TelegramToken != "tg-123" {
		t.Fatalf("rt.TelegramToken = %q", rt.TelegramToken)
	}
	if got := rt.Models[0].ProviderBaseURL; got != "https://api.moonshot.ai/v1" {
		t.Fatalf("rt.Models[0].ProviderBaseURL = %q", got)
	}
}

func TestLoadAgentRuntimesEmptyConfigReturnsNoRuntimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# starter config\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtimes, err := LoadAgentRuntimes(path)
	if err != nil {
		t.Fatalf("LoadAgentRuntimes() error = %v", err)
	}
	if len(runtimes) != 0 {
		t.Fatalf("len(runtimes) = %d, want 0", len(runtimes))
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

func TestValidateRequiresExecutionConfig(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{
			Name:    "moonshot",
			Type:    "openai-compatible",
			BaseURL: "https://api.moonshot.ai/v1",
			KeyEnv:  "MOONSHOT_API_KEY",
		}},
		Models: []Model{testModel("kimi-k2.5", "moonshot")},
		Agents: []Agent{{
			Name:   "Jared",
			Models: []string{"kimi-k2.5"},
			Workspace: Workspace{
				LocalDir: "/tmp/q15-workspaces/jared",
			},
			Telegram: Telegram{
				Token:          "tg-123",
				AllowedUserIDs: []int64{123456789},
			},
		}},
	}

	if err := cfg.Validate(); err == nil ||
		!strings.Contains(err.Error(), "agent[0].execution: is required") {
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

[[agent]]
name = "Jared"
models = ["kimi-k2.5"]

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

func TestValidateAllowsOpenAICodexProviderWithoutBaseURLOrKeyEnv(t *testing.T) {
	cfg := Config{
		Providers: []Provider{{
			Name: "openai-sub",
			Type: "openai-codex",
		}},
		Models: []Model{testModel("gpt-5-codex", "openai-sub")},
		Agents: []Agent{{
			Name:   "codex-agent",
			Models: []string{"gpt-5-codex"},
			Workspace: Workspace{
				LocalDir: "/tmp/q15-workspaces/codex",
			},
			Execution: &Execution{
				ServiceAddress: "exec-service:50051",
			},
			Telegram: Telegram{
				Token:          "tg-123",
				AllowedUserIDs: []int64{123456789},
			},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
