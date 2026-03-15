package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureStarterConfigCreatesMissingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "q15", "config.toml")

	seeded, err := ensureStarterConfig(configPath)
	if err != nil {
		t.Fatalf("ensureStarterConfig() error = %v", err)
	}
	if !seeded {
		t.Fatalf("ensureStarterConfig() seeded = false, want true")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read starter config: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "# q15 starter config") {
		t.Fatalf("starter config missing expected header: %q", got)
	}
	if !strings.Contains(got, "# [[provider]]") {
		t.Fatalf("starter config missing commented provider block: %q", got)
	}
	if !strings.Contains(got, "# [[model]]") {
		t.Fatalf("starter config missing commented model block: %q", got)
	}
	if !strings.Contains(got, "# [agent]") {
		t.Fatalf("starter config missing commented agent block: %q", got)
	}
	if got != starterConfigTemplate {
		t.Fatalf("starter config contents differ from embedded template:\n%s", got)
	}
}

func TestEnsureStarterConfigKeepsExistingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	original := "existing = true\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write original config: %v", err)
	}

	seeded, err := ensureStarterConfig(configPath)
	if err != nil {
		t.Fatalf("ensureStarterConfig() error = %v", err)
	}
	if seeded {
		t.Fatalf("ensureStarterConfig() seeded = true, want false")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read original config: %v", err)
	}
	if string(data) != original {
		t.Fatalf("config changed unexpectedly: %q", data)
	}
}

func TestStartSeedsMissingConfigAndReturnsNilWithNoAgent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "config.toml")

	if err := Start(context.Background(), configPath); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("seeded config missing: %v", err)
	}
}
