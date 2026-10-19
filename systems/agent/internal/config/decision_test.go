package config

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

func TestSystemOneProviderConfigAndSecretResolution(t *testing.T) {
	t.Setenv("DECISION_TEST_KEY", "test-key")
	cfg, err := Parse(
		[]byte(
			"providers:\n  - name: decisions\n    type: systemone-compatible\n    base_url: https://decisions.example/custom/v1\n    key_env: DECISION_TEST_KEY\n",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ResolveProviderAPIKey(cfg.Providers[0], providertypes.SystemOneCompatible)
	if err != nil || key != "test-key" {
		t.Fatalf("key = %q, %v", key, err)
	}
	for _, base := range []string{"", "://invalid", "https://decisions.example/v1?key=secret"} {
		cfg.Providers[0].BaseURL = base
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted base URL %q", base)
		}
	}
}
