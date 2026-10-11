package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRuntimeResolvesProxyEnvAndPolicyRevision(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_test_123")

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  no_proxy:
    - localhost
    - 127.0.0.1
  set_lowercase_proxy_env: true
  secrets:
    - github_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
  env:
    - name: GH_TOKEN
      secret: github_token
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
	if got := runtime.SecretValues["github_token"]; got != "ghp_test_123" {
		t.Fatalf("SecretValues[github_token] = %q, want %q", got, "ghp_test_123")
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
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN_FILE", secretPath)

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  secrets:
    - github_token
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
	if runtime.SecretValues["github_token"] != "ghp_file_123" {
		t.Fatalf(
			"SecretValues[github_token] = %q, want %q",
			runtime.SecretValues["github_token"],
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

func TestLoadRuntimeResolvesOCISigningRule(t *testing.T) {
	t.Setenv("OCI_API_KEY", "-----BEGIN PRIVATE KEY-----\nplaceholder\n-----END PRIVATE KEY-----")

	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(`
proxy:
  secrets:
    - oci_api_key
  rules:
    - name: oci-api
      match_hosts:
        - iaas.eu-frankfurt-1.oraclecloud.com
      sign_oci:
        secret: OCI_API_KEY
        key_id: ocid1.tenancy.oc1..t/ocid1.user.oc1..u/20:3B:97:13:55:1C:5B:0D:D3:37:D8:50:4E:C5:3A:34
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runtime, err := LoadRuntime(path)
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if len(runtime.Rules) != 1 {
		t.Fatalf("expected one rule, got %#v", runtime.Rules)
	}
	sign := runtime.Rules[0].SignOCI
	if sign == nil {
		t.Fatal("expected the signing rule to survive the load")
	}
	if sign.Secret != "oci_api_key" {
		t.Fatalf("sign_oci.secret = %q, want the normalized alias", sign.Secret)
	}
	if !strings.HasPrefix(sign.KeyID, "ocid1.tenancy.oc1..t/") {
		t.Fatalf("sign_oci.key_id = %q, want it kept", sign.KeyID)
	}
}

func TestValidateOCISigningRules(t *testing.T) {
	const fingerprint = "20:3B:97:13:55:1C:5B:0D:D3:37:D8:50:4E:C5:3A:34"

	tests := []struct {
		name    string
		rule    ProxyRule
		wantErr string
	}{
		{
			name: "valid",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  "ocid1.t/ocid1.u/" + fingerprint,
				},
			},
		},
		{
			name: "secret is not declared",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "other_secret",
					KeyID:  "ocid1.t/ocid1.u/" + fingerprint,
				},
			},
			wantErr: `sign_oci.secret "other_secret" is not defined in proxy.secrets`,
		},
		{
			name: "secret alias is not usable as an alias",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "not an alias",
					KeyID:  "ocid1.t/ocid1.u/" + fingerprint,
				},
			},
			wantErr: "alias must contain only a-z, 0-9, _, or -",
		},
		{
			name: "key id is not a triple",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI:    &ProxyOCISign{Secret: "oci_api_key", KeyID: fingerprint},
			},
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name: "key id has an empty part",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI:    &ProxyOCISign{Secret: "oci_api_key", KeyID: "ocid1.t/ /" + fingerprint},
			},
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name: "key id fingerprint is the right length but not hex",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  "ocid1.t/ocid1.u/" + strings.Repeat("z", 32),
				},
			},
			wantErr: "must be the 16 colon separated hex pairs of an MD5 fingerprint",
		},
		{
			name: "key id fingerprint is not hex",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  "ocid1.t/ocid1.u/" + strings.Repeat("z", 47),
				},
			},
			wantErr: "must be the 16 colon separated hex pairs of an MD5 fingerprint",
		},
		{
			name: "key id ocid uses a character outside the ocid alphabet",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  `ocid1.t"/ocid1.u/` + fingerprint,
				},
			},
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name: "signing and set_basic_auth",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  "ocid1.t/ocid1.u/" + fingerprint,
				},
				SetBasicAuth: &ProxyBasicAuth{Username: "user", Secret: "oci_api_key"},
			},
			wantErr: "cannot sign requests and also set an Authorization header",
		},
		{
			name: "signing and set_header Authorization",
			rule: ProxyRule{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI: &ProxyOCISign{
					Secret: "oci_api_key",
					KeyID:  "ocid1.t/ocid1.u/" + fingerprint,
				},
				SetHeader: map[string]string{"Authorization": "Bearer x"},
			},
			wantErr: "cannot sign requests and also set an Authorization header",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Proxy: Proxy{
					Secrets: []string{"oci_api_key"},
					Rules:   []ProxyRule{test.rule},
				},
			}

			err := cfg.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestNormalizeProxyRulesCarriesOCISigning(t *testing.T) {
	if got := normalizeProxyRules(nil); got != nil {
		t.Fatalf("normalizeProxyRules(nil) = %#v, want nil", got)
	}

	normalized := normalizeProxyRules([]ProxyRule{
		{
			Name:       " oci-api ",
			MatchHosts: []string{" IAAS.EU-FRANKFURT-1.ORACLECLOUD.COM "},
			SignOCI: &ProxyOCISign{
				Secret: " OCI_API_KEY ",
				KeyID:  "  ocid1.t/ocid1.u/20:3B:97:13  ",
			},
			SetHeader:    map[string]string{" authorization ": "Bearer x"},
			SetBasicAuth: &ProxyBasicAuth{Username: " user ", Secret: " OCI_API_KEY "},
			ReplacePlaceholder: []ProxyPlaceholderReplacement{
				{Placeholder: " __P__ ", Secret: " OCI_API_KEY ", In: []string{" HEADER "}},
			},
		},
	})

	if len(normalized) != 1 {
		t.Fatalf("expected one rule, got %#v", normalized)
	}
	if normalized[0].Name != "oci-api" {
		t.Fatalf("name = %q, want it trimmed", normalized[0].Name)
	}
	if normalized[0].MatchHosts[0] != "iaas.eu-frankfurt-1.oraclecloud.com" {
		t.Fatalf("match host = %q, want it lower cased", normalized[0].MatchHosts[0])
	}
	sign := normalized[0].SignOCI
	if sign == nil {
		t.Fatal("expected the signing block to survive normalization")
	}
	if sign.Secret != "oci_api_key" {
		t.Fatalf("secret = %q, want it trimmed and lower cased", sign.Secret)
	}
	if sign.KeyID != "ocid1.t/ocid1.u/20:3B:97:13" {
		t.Fatalf("key id = %q, want it trimmed", sign.KeyID)
	}
	if _, ok := normalized[0].SetHeader["Authorization"]; !ok {
		t.Fatalf("set_header keys must be canonicalized, got %#v", normalized[0].SetHeader)
	}
	if normalized[0].SetBasicAuth.Username != "user" ||
		normalized[0].SetBasicAuth.Secret != "oci_api_key" {
		t.Fatalf("set_basic_auth = %#v, want it trimmed", normalized[0].SetBasicAuth)
	}
	replacement := normalized[0].ReplacePlaceholder[0]
	if replacement.Placeholder != "__P__" || replacement.Secret != "oci_api_key" ||
		replacement.In[0] != "header" {
		t.Fatalf("replace_placeholder = %#v, want it normalized", replacement)
	}

	if got := normalizeProxyRules([]ProxyRule{{Name: "plain"}})[0].SignOCI; got != nil {
		t.Fatalf("expected no signing block on a rule without one, got %#v", got)
	}
}

func TestPolicyRevisionTracksOCISigning(t *testing.T) {
	t.Setenv("OCI_API_KEY", "placeholder")

	resolve := func(keyID string) string {
		t.Helper()

		runtime, err := Config{
			Proxy: Proxy{
				Secrets: []string{"oci_api_key"},
				Rules: []ProxyRule{
					{
						Name:       "oci-api",
						MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
						SignOCI: &ProxyOCISign{
							Secret: "oci_api_key",
							KeyID:  "ocid1.t/ocid1.u/" + keyID,
						},
					},
				},
			},
		}.ResolveRuntime()
		if err != nil {
			t.Fatalf("ResolveRuntime() error = %v", err)
		}
		return runtime.PolicyRevision
	}

	first := resolve("20:3B:97:13:55:1C:5B:0D:D3:37:D8:50:4E:C5:3A:34")
	second := resolve("AA:3B:97:13:55:1C:5B:0D:D3:37:D8:50:4E:C5:3A:34")

	if first == "" || second == "" {
		t.Fatal("expected a policy revision for both configs")
	}
	if first == second {
		t.Fatal("expected a different policy revision when the signing key id changes")
	}
}
