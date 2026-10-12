package policy

import (
	"strings"
	"testing"
)

func validPolicy() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Proxy: Proxy{
			NoProxy:              []string{"localhost"},
			SetLowercaseProxyEnv: true,
			Secrets:              []string{"github_token"},
			Rules: []ProxyRule{
				{Name: "github-api", MatchHosts: []string{"api.github.com"}},
			},
			Env: []ProxyEnv{
				{
					Name:   "GH_TOKEN",
					Secret: "github_token",
					Rules:  []string{"github-api"},
					In:     []string{"header"},
				},
			},
		},
	}
}

func TestValidateAcceptsAWrittenPolicy(t *testing.T) {
	if err := validPolicy().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// An absent version is the version this build reads: a policy written before the
// field existed is not an older policy.
func TestValidateAcceptsAnAbsentSchemaVersion(t *testing.T) {
	cfg := validPolicy()
	cfg.SchemaVersion = 0

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRefusesANewerSchemaVersion(t *testing.T) {
	cfg := validPolicy()
	cfg.SchemaVersion = SchemaVersion + 1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a schema version error")
	}
	if !strings.Contains(err.Error(), "newer than this build reads") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiresADeclaredSecret(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Secrets = nil

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a secrets error")
	}
	if !strings.Contains(err.Error(), "secrets must contain at least one alias") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsAnUndeclaredSecretInAnEnvMapping(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Env[0].Secret = "other_token"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an undeclared secret error")
	}
	if !strings.Contains(err.Error(), "is not defined in proxy.secrets") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsInvalidProxyEnvRuleReference(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Env[0].Rules = []string{"missing-rule"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "does not match any proxy rule name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsAnAmbiguousRuleNameReference(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Rules = append(cfg.Proxy.Rules, ProxyRule{
		Name:       "github-api",
		MatchHosts: []string{"github.com"},
	})

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "matches multiple proxy rules") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsAHostSubstitutionInTheBody(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Rules[0].ReplacePlaceholder = []ProxyPlaceholderReplacement{
		{Placeholder: "__TOKEN__", Secret: "github_token", In: []string{"body"}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "body is not supported in v1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsABasicAuthThatAlsoSetsTheHeader(t *testing.T) {
	cfg := validPolicy()
	cfg.Proxy.Rules[0].SetHeader = map[string]string{"authorization": "Bearer x"}
	cfg.Proxy.Rules[0].SetBasicAuth = &ProxyBasicAuth{Username: "user", Secret: "github_token"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "cannot set both set_basic_auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizedSecretsAreLowerCasedAndDeduplicated(t *testing.T) {
	proxy := Proxy{Secrets: []string{" GitHub_Token ", "github_token", "brave-api-key"}}

	got, err := proxy.NormalizedSecrets()
	if err != nil {
		t.Fatalf("NormalizedSecrets() error = %v", err)
	}
	want := []string{"github_token", "brave-api-key"}
	if len(got) != len(want) {
		t.Fatalf("NormalizedSecrets() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizedSecrets() = %v, want %v", got, want)
		}
	}
}

func TestNormalizedSecretsRejectAnUnusableAlias(t *testing.T) {
	proxy := Proxy{Secrets: []string{"gh token"}}

	if _, err := proxy.NormalizedSecrets(); err == nil {
		t.Fatal("expected an alias error")
	}
}

func TestNormalizedRulesCanonicalizeHeadersAndSecrets(t *testing.T) {
	proxy := Proxy{Rules: []ProxyRule{{
		Name:              " github-api ",
		MatchHosts:        []string{" API.GitHub.com ", "API.github.com"},
		MatchPathPrefixes: []string{" /repos "},
		SetHeader:         map[string]string{"x-token": "  value  "},
		SetBasicAuth:      &ProxyBasicAuth{Username: " user ", Secret: " GitHub_Token "},
	}}}

	got := proxy.NormalizedRules()
	if len(got) != 1 {
		t.Fatalf("NormalizedRules() = %#v, want one rule", got)
	}
	rule := got[0]
	if rule.Name != "github-api" {
		t.Fatalf("Name = %q, want %q", rule.Name, "github-api")
	}
	if len(rule.MatchHosts) != 1 || rule.MatchHosts[0] != "api.github.com" {
		t.Fatalf("MatchHosts = %v, want [api.github.com]", rule.MatchHosts)
	}
	if len(rule.MatchPathPrefixes) != 1 || rule.MatchPathPrefixes[0] != "/repos" {
		t.Fatalf("MatchPathPrefixes = %v, want [/repos]", rule.MatchPathPrefixes)
	}
	if value := rule.SetHeader["X-Token"]; value != "value" {
		t.Fatalf("SetHeader = %#v, want the header in its wire case", rule.SetHeader)
	}
	if rule.SetBasicAuth.Secret != "github_token" || rule.SetBasicAuth.Username != "user" {
		t.Fatalf("SetBasicAuth = %#v, want a canonical secret", rule.SetBasicAuth)
	}
}

// A mapping that names no place for its value puts it where a bearer token
// belongs, so a policy that says only "GH_TOKEN comes from github_token" is a
// complete one.
func TestNormalizedEnvDefaultsToTheRequestHeader(t *testing.T) {
	proxy := Proxy{Env: []ProxyEnv{{Name: "GH_TOKEN", Secret: "github_token"}}}

	got := proxy.NormalizedEnv()
	if len(got) != 1 {
		t.Fatalf("NormalizedEnv() = %#v, want one mapping", got)
	}
	if len(got[0].In) != 1 || got[0].In[0] != "header" {
		t.Fatalf("In = %v, want [header]", got[0].In)
	}
}

func TestDecodeRefusesAnUnknownField(t *testing.T) {
	_, err := Decode([]byte("proxy:\n  unknown_field: true\n"))
	if err == nil {
		t.Fatal("expected an unknown field error")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeRefusesASecondDocument(t *testing.T) {
	_, err := Decode([]byte("proxy:\n  secrets:\n    - github_token\n---\nproxy:\n"))
	if err == nil {
		t.Fatal("expected a multiple document error")
	}
	if !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeReadsAnEmptyDocumentAsAnEmptyPolicy(t *testing.T) {
	cfg, err := Decode(nil)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	err = cfg.Validate()
	if err == nil {
		t.Fatal("expected an empty policy to fail validation")
	}
	if !strings.Contains(err.Error(), "secrets must contain at least one alias") {
		t.Fatalf("unexpected error: %v", err)
	}
}
