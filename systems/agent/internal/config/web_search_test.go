package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentRuntimeYAMLOllamaWebSearchKey(t *testing.T) {
	for _, source := range []string{"env", "file", "missing"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("Q15_TELEGRAM_TOKEN", "tg")
			t.Setenv("TEST_OLLAMA_SEARCH_KEY", "")
			t.Setenv("TEST_OLLAMA_SEARCH_KEY_FILE", "")
			dir := t.TempDir()
			switch source {
			case "env":
				t.Setenv("TEST_OLLAMA_SEARCH_KEY", "ollama-search-key")
			case "file":
				keyPath := filepath.Join(dir, "ollama-key")
				if err := os.WriteFile(keyPath, []byte("ollama-search-key\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TEST_OLLAMA_SEARCH_KEY_FILE", keyPath)
			}

			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(`
providers:
  - name: p
    type: ollama
agent:
  name: a
  tools:
    web_search:
      ollama_api_key_env: TEST_OLLAMA_SEARCH_KEY
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids: [1]
`), 0o600); err != nil {
				t.Fatal(err)
			}

			rt, err := LoadAgentRuntime(path)
			if source == "missing" {
				if err == nil || !strings.Contains(err.Error(), "TEST_OLLAMA_SEARCH_KEY") {
					t.Fatalf("LoadAgentRuntime() error = %v, want missing Ollama key", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rt.Tools.WebSearch.OllamaAPIKey != "ollama-search-key" {
				t.Fatal("runtime did not resolve the Ollama search key")
			}
			if rt.Tools.WebSearch.BraveAPIKey != "" {
				t.Fatal("Ollama-only configuration enabled Brave")
			}
		})
	}
}
