package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRuntimeResolvesProxyEnvAndPolicyRevision(t *testing.T) {
	t.Setenv("JARED_GH_TOKEN", "ghp_test_123")

	path := filepath.Join(t.TempDir(), "proxy.toml")
	if err := os.WriteFile(path, []byte(`
[service]
admin_listen = "127.0.0.1:50052"
proxy_listen = "127.0.0.1:18080"
advertise_proxy_url = "http://proxy:18080"
state_dir = "/tmp/q15-proxy-service"
version = "test"

[proxy]
no_proxy = ["localhost", "127.0.0.1"]
set_lowercase_proxy_env = true
secrets = ["jared_gh_token"]

[[proxy.rule]]
name = "github-api"
match_hosts = ["api.github.com"]

[[proxy.env]]
name = "GH_TOKEN"
secret = "jared_gh_token"
rules = ["github-api"]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadRuntime(path)
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if runtime.ServiceVersion != "test" {
		t.Fatalf("unexpected service version: %q", runtime.ServiceVersion)
	}
	if runtime.AdvertiseProxyURL != "http://proxy:18080" {
		t.Fatalf("unexpected advertise proxy url: %q", runtime.AdvertiseProxyURL)
	}
	if runtime.NoProxy != "localhost,127.0.0.1" {
		t.Fatalf("unexpected no_proxy: %q", runtime.NoProxy)
	}
	if got := runtime.SecretValues["jared_gh_token"]; got != "ghp_test_123" {
		t.Fatalf("unexpected secret value: %q", got)
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
	if got := runtime.Rules[0].ReplacePlaceholder[0].Placeholder; got != placeholder {
		t.Fatalf("unexpected expanded placeholder: %q", got)
	}
	if runtime.PolicyRevision == "" {
		t.Fatalf("expected policy revision")
	}

	runtime2, err := LoadRuntime(path)
	if err != nil {
		t.Fatalf("LoadRuntime(second) error = %v", err)
	}
	if runtime.PolicyRevision != runtime2.PolicyRevision {
		t.Fatalf("expected stable policy revision across loads")
	}
	if runtime.EnvValues["GH_TOKEN"] != runtime2.EnvValues["GH_TOKEN"] {
		t.Fatalf("expected stable env placeholder across loads")
	}
}

func TestLoadRuntimeRequiresSecretEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.toml")
	if err := os.WriteFile(path, []byte(`
[service]
admin_listen = "127.0.0.1:50052"
proxy_listen = "127.0.0.1:18080"
advertise_proxy_url = "http://proxy:18080"
state_dir = "/tmp/q15-proxy-service"

[proxy]
secrets = ["gh_token"]

[[proxy.rule]]
name = "github-api"
match_hosts = ["api.github.com"]
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadRuntime(path); err == nil {
		t.Fatalf("expected missing secret env error")
	}
}

func TestValidateRejectsInvalidProxyEnvRuleReference(t *testing.T) {
	cfg := Config{
		Service: Service{
			AdminListen:       "127.0.0.1:50052",
			ProxyListen:       "127.0.0.1:18080",
			AdvertiseProxyURL: "http://proxy:18080",
			StateDir:          "/tmp/q15-proxy-service",
		},
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
		t.Fatalf("expected validation error")
	} else if !strings.Contains(err.Error(), "does not match any proxy rule name") {
		t.Fatalf("unexpected error: %v", err)
	}
}
