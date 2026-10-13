package stack

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/q15co/q15/libs/proxy-contract/policy"
)

func TestProxyPolicyYAMLNamesTheStacksOwnServices(t *testing.T) {
	profile := DefaultProfile("1.2.3", "/state")
	profile.AgentName = "Demo Two"

	data, err := profile.ProxyPolicyYAML()
	if err != nil {
		t.Fatalf("ProxyPolicyYAML() error = %v", err)
	}

	cfg, err := policy.Decode(data)
	if err != nil {
		t.Fatalf("decode rendered policy: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate rendered policy: %v", err)
	}

	if cfg.SchemaVersion != policy.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", cfg.SchemaVersion, policy.SchemaVersion)
	}
	wantNoProxy := []string{"localhost", "127.0.0.1", "::1", "demo-two-proxy", "demo-two-exec"}
	if !slices.Equal(cfg.Proxy.NoProxy, wantNoProxy) {
		t.Fatalf("no_proxy = %v, want %v", cfg.Proxy.NoProxy, wantNoProxy)
	}
	if got := cfg.Proxy.Secrets; !slices.Equal(got, []string{githubTokenFile}) {
		t.Fatalf("secrets = %v, want [%s]", got, githubTokenFile)
	}
	if len(cfg.Proxy.Rules) != 1 || cfg.Proxy.Rules[0].Name != githubAPIRule {
		t.Fatalf("rules = %#v, want one %q rule", cfg.Proxy.Rules, githubAPIRule)
	}
	if len(cfg.Proxy.Env) != 1 || cfg.Proxy.Env[0].Name != "GH_TOKEN" {
		t.Fatalf("env = %#v, want one GH_TOKEN mapping", cfg.Proxy.Env)
	}
	if got := cfg.Proxy.Env[0].Secret; got != githubTokenFile {
		t.Fatalf("env secret = %q, want %q", got, githubTokenFile)
	}
}

// The checked-in Compose policy is what the proxy runs today, and it is the
// shape a rendered policy has to replace rather than sit beside. Comparing the
// decoded documents rather than their bytes keeps the test about the policy and
// not about YAML formatting.
func TestProxyPolicyMatchesTheShippedComposePolicy(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "deploy", "compose", "proxy-policy.yaml")
	shipped, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	want, err := policy.Decode(shipped)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	profile := DefaultProfile("1.2.3", "/state")
	got, err := profile.ProxyPolicy()
	if err != nil {
		t.Fatalf("ProxyPolicy() error = %v", err)
	}

	if len(got.Proxy.Rules) != len(want.Proxy.Rules) ||
		got.Proxy.Rules[0].Name != want.Proxy.Rules[0].Name {
		t.Fatalf("rendered rules = %#v, shipped rules = %#v", got.Proxy.Rules, want.Proxy.Rules)
	}
	if !slices.Equal(got.Proxy.NoProxy, want.Proxy.NoProxy) {
		t.Fatalf(
			"rendered no_proxy = %v, shipped no_proxy = %v",
			got.Proxy.NoProxy,
			want.Proxy.NoProxy,
		)
	}
	if !slices.Equal(got.Proxy.Secrets, want.Proxy.Secrets) {
		t.Fatalf(
			"rendered secrets = %v, shipped secrets = %v",
			got.Proxy.Secrets,
			want.Proxy.Secrets,
		)
	}
	if got.Proxy.SetLowercaseProxyEnv != want.Proxy.SetLowercaseProxyEnv {
		t.Fatalf(
			"rendered set_lowercase_proxy_env = %v, shipped = %v",
			got.Proxy.SetLowercaseProxyEnv,
			want.Proxy.SetLowercaseProxyEnv,
		)
	}
	if len(got.Proxy.Env) != len(want.Proxy.Env) {
		t.Fatalf("rendered env = %#v, shipped env = %#v", got.Proxy.Env, want.Proxy.Env)
	}
	for i := range got.Proxy.Env {
		if got.Proxy.Env[i].Name != want.Proxy.Env[i].Name ||
			got.Proxy.Env[i].Secret != want.Proxy.Env[i].Secret ||
			!slices.Equal(got.Proxy.Env[i].Rules, want.Proxy.Env[i].Rules) ||
			!slices.Equal(got.Proxy.Env[i].In, want.Proxy.Env[i].In) {
			t.Fatalf(
				"rendered env[%d] = %#v, shipped = %#v",
				i,
				got.Proxy.Env[i],
				want.Proxy.Env[i],
			)
		}
	}
}
