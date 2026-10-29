package config

import (
	"math"
	"net/url"
	"strings"
	"testing"
)

func TestHistoryDefaultsAndIndependentModelBinding(t *testing.T) {
	t.Setenv("Q15_DATABASE_URL", "postgresql://fixture/database")
	t.Setenv("OLLAMA_API_KEY", "cloud-subscription")
	cfg := Config{
		Agent: &Agent{Name: "fixture"},
		Providers: []Provider{
			{
				Name:    "cloud",
				Type:    "ollama",
				BaseURL: "https://ollama.com",
				KeyEnv:  "OLLAMA_API_KEY",
			},
		},
	}
	h, err := cfg.ResolveHistory()
	if err != nil {
		t.Fatal(err)
	}
	if h.SummaryProvider != "cloud" || h.SummaryModel != "minimax-m3.1-flash" ||
		h.EmbeddingModel != "embeddinggemma-2:740m" ||
		h.EmbeddingProvider != "ollama" ||
		h.EmbeddingBatchTokens != 8192 ||
		h.DatabaseURL == "" ||
		h.InitialContextMaxTokens != 100000 || h.InitialContextMaxFraction != 0.25 || h.RecentTurns != 4 {
		t.Fatal("history defaults or explicit binding differ")
	}
	cfg.Agent.History.InitialContextMaxTokens = 32000
	cfg.Agent.History.InitialContextMaxFraction = 0.5
	cfg.Agent.History.EmbeddingProvider = "openai"
	cfg.Agent.History.EmbeddingBatchTokens = 16384
	cfg.Agent.History.EmbeddingBaseURL = "http://tei:80"
	h, err = cfg.ResolveHistory()
	if err != nil || h.InitialContextMaxTokens != 32000 || h.InitialContextMaxFraction != 0.5 ||
		h.EmbeddingProvider != "openai" || h.EmbeddingBatchTokens != 16384 {
		t.Fatal("history overrides failed", err)
	}
}

func TestDatabaseUsesOneSharedPasswordSecret(t *testing.T) {
	t.Setenv("Q15_DATABASE_URL", "")
	t.Setenv("Q15_DATABASE_URL_FILE", "")
	t.Setenv("PGHOST", "postgres")
	t.Setenv("PGUSER", "q15")
	t.Setenv("PGDATABASE", "q15")
	t.Setenv("PGPASSWORD", "a:@/password")
	t.Setenv("PGSSLMODE", "disable")
	dsn, err := ResolveDatabaseURL("Q15_DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.Host != "postgres" || parsed.User.Username() != "q15" || password != "a:@/password" ||
		parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("connection fields did not round-trip")
	}
}

func TestRetiredConfigKeysAreRejected(t *testing.T) {
	for _, key := range []string{"memory_recent_turns", "memory_working_budget_bytes", "memory_open_loops_budget_bytes"} {
		if _, err := Parse([]byte("providers: []\nagent:\n  name: fixture\n  " + key + ": 8192\n")); err == nil {
			t.Fatalf("retired config key %s accepted", key)
		}
	}
}

func TestHistoryLocalSummariesRequireExplicitBinding(t *testing.T) {
	t.Setenv("PGHOST", "")
	cfg := Config{
		Agent: &Agent{
			Name:    "fixture",
			History: History{SummaryProvider: "local", SummaryModel: "qwen3:8b"},
		},
		Providers: []Provider{{Name: "local", Type: "ollama", BaseURL: "http://ollama:11434"}},
	}
	h, err := cfg.ResolveHistory()
	if err != nil {
		t.Fatal(err)
	}
	if h.SummaryBaseURL != "http://ollama:11434" || h.SummaryModel != "qwen3:8b" ||
		h.SummaryAPIKey != "" {
		t.Fatal("explicit local binding not preserved")
	}
	cfg.Agent.History.SummaryProvider = ""
	h, err = cfg.ResolveHistory()
	if err != nil {
		t.Fatal(err)
	}
	if h.SummaryBaseURL != "" {
		t.Fatal("summary model silently inherited an arbitrary local provider")
	}
}

func TestHistoryRejectsInvalidBudgetPolicy(t *testing.T) {
	for _, bad := range []History{
		{InitialContextMaxFraction: -0.1}, {InitialContextMaxFraction: 1.1}, {InitialContextMaxFraction: math.NaN()},
		{InitialContextMaxFraction: math.Inf(1)},
		{InitialContextMaxTokens: -1}, {InitialContextMaxTokens: 512}, {RecentTurns: 33},
		{EmbeddingBatchTokens: -1}, {EmbeddingBatchTokens: 65537},
	} {
		cfg := Config{Agent: &Agent{Name: "fixture", History: bad}}
		if _, err := cfg.ResolveHistory(); err == nil {
			t.Fatalf("invalid policy accepted: %+v", bad)
		}
	}
}

func TestHistoryParsesInitialContextCapsAndRejectsRetiredKeys(t *testing.T) {
	const prefix = `providers:
  - name: fixture
    type: ollama
agent:
  name: fixture
  history:
`
	cfg, err := Parse(
		[]byte(
			prefix + "    initial_context_max_tokens: 100000\n    initial_context_max_fraction: 0.25\n",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.History.InitialContextMaxTokens != 100000 ||
		cfg.Agent.History.InitialContextMaxFraction != 0.25 {
		t.Fatal("initial context caps did not retain their units")
	}
	for key, value := range map[string]string{
		"working_budget_tokens":   "64000",
		"context_window_fraction": "0.25",
		"model_budget_tokens":     "{cloud/model: 64000}",
		"recent_history_fraction": "0.3333333333333333",
		"max_request_bytes":       "0",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := Parse(
				[]byte(
					prefix + "    " + key + ": " + value + "\n",
				),
			)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("retired key was not rejected explicitly: %v", err)
			}
		})
	}
}
