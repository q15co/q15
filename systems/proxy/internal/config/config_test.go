package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRuntimeResolvesProxyEnvAndPolicyRevision(t *testing.T) {
	t.Setenv("JARED_GH_TOKEN", "ghp_test_123")

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  no_proxy:
    - localhost
    - 127.0.0.1
  set_lowercase_proxy_env: true
  secrets:
    - jared_gh_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
  env:
    - name: GH_TOKEN
      secret: jared_gh_token
      rules:
        - github-api
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadRuntime(path)
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if runtime.ServiceVersion != "dev" {
		t.Fatalf("ServiceVersion = %q, want %q", runtime.ServiceVersion, "dev")
	}
	if runtime.AdminListen != ":50052" {
		t.Fatalf("AdminListen = %q, want %q", runtime.AdminListen, ":50052")
	}
	if runtime.ProxyListen != ":18080" {
		t.Fatalf("ProxyListen = %q, want %q", runtime.ProxyListen, ":18080")
	}
	if runtime.AdvertiseProxyURL != "http://q15-proxy:18080" {
		t.Fatalf(
			"AdvertiseProxyURL = %q, want %q",
			runtime.AdvertiseProxyURL,
			"http://q15-proxy:18080",
		)
	}
	if runtime.StateDir != "/var/lib/q15/proxy" {
		t.Fatalf("StateDir = %q, want %q", runtime.StateDir, "/var/lib/q15/proxy")
	}
	if runtime.NoProxy != "localhost,127.0.0.1" {
		t.Fatalf("NoProxy = %q, want %q", runtime.NoProxy, "localhost,127.0.0.1")
	}
	if got := runtime.SecretValues["jared_gh_token"]; got != "ghp_test_123" {
		t.Fatalf("SecretValues[jared_gh_token] = %q, want %q", got, "ghp_test_123")
	}
	placeholder := runtime.EnvValues["GH_TOKEN"]
	if !strings.HasPrefix(placeholder, "__Q15_PROXY_ENV_") {
		t.Fatalf("unexpected placeholder: %q", placeholder)
	}
	if placeholder == "ghp_test_123" {
		t.Fatalf("placeholder should not equal the real secret")
	}
	if len(runtime.Rules) != 1 || len(runtime.Rules[0].ReplacePlaceholder) != 1 {
		t.Fatalf("expected expanded placeholder replacement, got %#v", runtime.Rules)
	}
	if runtime.PolicyRevision == "" {
		t.Fatal("expected policy revision")
	}
}

func TestLoadRuntimeRequiresSecretEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  secrets:
    - gh_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadRuntime(path); err == nil {
		t.Fatal("expected missing secret env error")
	}
}

func TestLoadRuntimeReadsSecretFromFileEnv(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "gh-token.txt")
	if err := os.WriteFile(secretPath, []byte("ghp_file_123\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	t.Setenv("JARED_GH_TOKEN", "")
	t.Setenv("JARED_GH_TOKEN_FILE", secretPath)

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  secrets:
    - jared_gh_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadRuntime(path)
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if runtime.SecretValues["jared_gh_token"] != "ghp_file_123" {
		t.Fatalf(
			"SecretValues[jared_gh_token] = %q, want %q",
			runtime.SecretValues["jared_gh_token"],
			"ghp_file_123",
		)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  unknown_field: true
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestValidateRejectsInvalidProxyEnvRuleReference(t *testing.T) {
	cfg := Config{
		Proxy: Proxy{
			Secrets: []string{"gh_token"},
			Rules: []ProxyRule{
				{Name: "github-api", MatchHosts: []string{"api.github.com"}},
			},
			Env: []ProxyEnv{
				{Name: "GH_TOKEN", Secret: "gh_token", Rules: []string{"missing-rule"}},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	} else if !strings.Contains(err.Error(), "does not match any proxy rule name") {
		t.Fatalf("unexpected error: %v", err)
	}
}
