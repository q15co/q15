package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// baseURLCatalog answers discovery by base URL, so a test can see exactly which
// provider list the registry is discovering against, and count the attempts.
type baseURLCatalog struct {
	models     map[string][]modelcatalog.Model
	asked      []string
	onDiscover func(string)
}

func (c *baseURLCatalog) Discover(
	_ context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	c.asked = append(c.asked, p.BaseURL)
	if c.onDiscover != nil {
		c.onDiscover(p.BaseURL)
	}
	return c.models[p.BaseURL], nil
}

func (c *baseURLCatalog) asks(baseURL string) int {
	n := 0
	for _, asked := range c.asked {
		if asked == baseURL {
			n++
		}
	}
	return n
}

// writeConfigFile writes a one-provider config pointing at baseURL.
func writeConfigFile(t *testing.T, path, baseURL string) {
	t.Helper()
	body := "providers:\n  - name: local\n    type: ollama\n    base_url: " + baseURL + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// reloadFixture builds a registry discovering against one base URL, plus a watcher
// over a config file that currently names it.
func reloadFixture(t *testing.T) (*configWatcher, *modelcatalog.Registry, *baseURLCatalog, string) {
	t.Helper()

	const first = "http://one.test:11434"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfigFile(t, path, first)

	catalog := &baseURLCatalog{models: map[string][]modelcatalog.Model{
		first: {
			{
				ProviderModel: "first",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
		"http://two.test:11434": {
			{
				ProviderModel: "second",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
	}}
	providers := []modelcatalog.Provider{{Name: "local", Type: "ollama", BaseURL: first}}
	registry := modelcatalog.New(providers, catalog, time.Hour, time.Second)
	registry.Refresh(context.Background())

	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher := newConfigWatcher(registry, path, initial)
	return watcher, registry, catalog, path
}

func TestConfigWatcher_UnchangedConfigIsNotAReload(t *testing.T) {
	watcher, registry, catalog, _ := reloadFixture(t)

	watcher.tick(context.Background())

	if got := catalog.asks("http://one.test:11434"); got != 2 {
		t.Errorf("discovery ran %d times, want 2: once at startup and once per tick", got)
	}
	if _, ok := registry.ReloadStatus(); ok {
		t.Error("no reload has been attempted, so there should be no reload status")
	}
}

func TestConfigWatcher_AppliesAnEditedProviderList(t *testing.T) {
	watcher, registry, catalog, path := reloadFixture(t)

	writeConfigFile(t, path, "http://two.test:11434")
	watcher.tick(context.Background())

	models := registry.Snapshot()
	if len(models) != 1 || models[0].ProviderModel != "second" {
		t.Fatalf("snapshot = %+v, want the roster discovered against the new base URL", models)
	}
	if got := catalog.asks("http://two.test:11434"); got != 1 {
		t.Errorf("the new base URL was asked %d times, want 1", got)
	}
	status, ok := registry.ReloadStatus()
	if !ok || !status.Applied || status.Error != "" {
		t.Fatalf("reload status = %+v (found=%v), want an applied reload", status, ok)
	}
	if _, ok := registry.ProviderStatus("local"); !ok {
		t.Error("the provider set in effect should report a discovery status")
	}
}

func TestConfigWatcher_RefusesAnEditItCannotLoad(t *testing.T) {
	watcher, registry, _, path := reloadFixture(t)

	body := "providers:\n  - name: local\n    type: not-a-provider\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	watcher.tick(context.Background())

	models := registry.Snapshot()
	if len(models) != 1 || models[0].ProviderModel != "first" {
		t.Fatalf("snapshot = %+v, want the running providers untouched", models)
	}
	status, ok := registry.ReloadStatus()
	if !ok {
		t.Fatal("a refused edit should be recorded")
	}
	if status.Applied {
		t.Error("a refused edit must not report as applied")
	}
	if !strings.Contains(status.Error, "not supported") {
		t.Errorf("reload error = %q, want the config error", status.Error)
	}

	// The same bad file is neither retried nor re-reported on every tick.
	refusedAt := status.CheckedAt
	watcher.tick(context.Background())
	again, _ := registry.ReloadStatus()
	if !again.CheckedAt.Equal(refusedAt) {
		t.Error("an unchanged bad file should not be re-attempted or re-reported")
	}
}

func TestConfigWatcher_RefusesAnEditWhoseSecretDisappeared(t *testing.T) {
	watcher, registry, _, path := reloadFixture(t)

	// A secret file that is no longer there: the edit must be refused whole,
	// because the providers already running still hold their keys.
	t.Setenv("Q15_TEST_RELOAD_KEY_FILE", filepath.Join(t.TempDir(), "gone"))
	body := "providers:\n  - name: remote\n    type: openai-compatible\n" +
		"    base_url: https://api.example.test/v1\n    key_env: Q15_TEST_RELOAD_KEY\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	watcher.tick(context.Background())

	models := registry.Snapshot()
	if len(models) != 1 || models[0].ProviderModel != "first" {
		t.Fatalf("snapshot = %+v, want the running providers untouched", models)
	}
	status, ok := registry.ReloadStatus()
	if !ok || status.Applied {
		t.Fatalf("reload status = %+v (found=%v), want a refusal", status, ok)
	}
	if !strings.Contains(status.Error, "Q15_TEST_RELOAD_KEY") {
		t.Errorf("reload error = %q, want it to name the missing secret", status.Error)
	}
}

func TestConfigWatcher_RefusesAnEditThatDiscoversNothing(t *testing.T) {
	watcher, registry, catalog, path := reloadFixture(t)
	catalog.onDiscover = func(baseURL string) {
		if providers := registry.Providers(); len(providers) != 1 ||
			providers[0].BaseURL != "http://one.test:11434" {
			t.Errorf("discovery of %q published unverified providers: %+v", baseURL, providers)
		}
		if models := registry.Snapshot(); len(models) != 1 || models[0].ProviderModel != "first" {
			t.Errorf("discovery of %q replaced the working roster: %+v", baseURL, models)
		}
	}

	// A config that loads and resolves but has nothing behind it: applying it
	// would leave the agent with no models, which is what startup refuses too.
	writeConfigFile(t, path, "http://dead.test:11434")
	watcher.tick(context.Background())

	models := registry.Snapshot()
	if len(models) != 1 || models[0].ProviderModel != "first" {
		t.Fatalf("snapshot = %+v, want the running providers restored", models)
	}
	status, ok := registry.ReloadStatus()
	if !ok || status.Applied {
		t.Fatalf("reload status = %+v (found=%v), want a refusal", status, ok)
	}
	if !strings.Contains(status.Error, "discovered no models") {
		t.Errorf("reload error = %q, want it to say the edit discovered nothing", status.Error)
	}

	// And the corrected file applies afterwards, so a refusal is not a latch.
	catalog.onDiscover = nil
	writeConfigFile(t, path, "http://two.test:11434")
	watcher.tick(context.Background())
	models = registry.Snapshot()
	if len(models) != 1 || models[0].ProviderModel != "second" {
		t.Fatalf("snapshot = %+v, want the corrected edit applied", models)
	}
}

func TestConfigWatcher_SeesEditsDuringStartupDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const first = "http://one.test:11434"
	const second = "http://two.test:11434"
	writeConfigFile(t, path, first)
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &baseURLCatalog{models: map[string][]modelcatalog.Model{
		first: {
			{
				ProviderModel: "first",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		}, second: {{ProviderModel: "second", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}}},
	}, onDiscover: func(baseURL string) {
		if baseURL == first {
			writeConfigFile(t, path, second)
		}
	}}
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "local", Type: "ollama", BaseURL: first}},
		catalog,
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	watcher := newConfigWatcher(registry, path, initial)
	watcher.tick(context.Background())
	if models := registry.Snapshot(); len(models) != 1 || models[0].ProviderModel != "second" {
		t.Fatalf("startup edit was missed: %+v", models)
	}
}
