package app

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	q15media "github.com/q15co/q15/systems/agent/internal/media"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	modeltools "github.com/q15co/q15/systems/agent/internal/tools/models"
)

func TestConfiguredDualBindIsLoggedAndSurfaced(t *testing.T) {
	registry := testRegistry(
		t,
		map[string][]modelcatalog.Model{
			"api": {
				{
					ProviderModel: "both",
					Capabilities:  modelcatalog.Capabilities{Chat: true, Decision: true},
				},
			},
		},
	)
	limits := config.RoleLimits{RequestsPerMinute: 1, RequestBytesPerMinute: 100}
	cfg := config.Config{
		Primary: &config.ModelBinding{Provider: "api", Model: "both", Limits: limits},
		Decision: &config.ModelBinding{
			Provider:      "api",
			Model:         "both",
			AllowDualBind: "separate quotas",
			Limits:        limits,
		},
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	for range 2 {
		if err := configureModelRoles(cfg, registry); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(output.String(), `allow_dual_bind="separate quotas"`) != 2 {
		t.Fatalf("startup log = %s", output.String())
	}
	status, err := modeltools.NewListProviders(registry, nil).Run(context.Background(), "{}")
	if err != nil || !strings.Contains(status, `"allow_dual_bind": "separate quotas"`) ||
		!strings.Contains(status, `"request_bytes_per_minute": 100`) {
		t.Fatalf("status = %s, %v", status, err)
	}
}

func TestProviderReloadCannotInvalidateRunningRole(t *testing.T) {
	watcher, registry, _, path := reloadFixture(t)
	cfg := config.Config{Primary: &config.ModelBinding{Provider: "local", Model: "first"}}
	if err := configureModelRoles(cfg, registry); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, path, "http://two.test:11434")
	watcher.tick(context.Background())
	if _, ok := registry.Lookup("local", "first"); !ok {
		t.Fatal("invalid replacement discarded the running binding")
	}
	status, ok := registry.ReloadStatus()
	if !ok || status.Applied || !strings.Contains(status.Error, "role primary") ||
		!strings.Contains(status.Error, "chat") {
		t.Fatalf("reload status = %+v", status)
	}
}

func TestConfiguredFallbackKeepsProviderIdentity(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"one": {{ProviderModel: "shared", Capabilities: modelcatalog.Capabilities{Chat: true}}},
		"two": {{ProviderModel: "shared", Capabilities: modelcatalog.Capabilities{Chat: true}}},
	})
	registry.SetRoleBindings(
		[]modelcatalog.RoleBinding{
			{Role: "primary", Provider: "one", Model: "shared"},
			{Role: "fallbacks[0]", Provider: "two", Model: "shared"},
		},
	)
	var called []string
	adapter, err := newModelAdapterWithSelectionAndFactory(
		registry,
		modelcatalog.NewSelection(registry, "one", "shared"),
		nil,
		func(m modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			called = append(called, m.ProviderName)
			if m.ProviderName == "one" {
				return nil, fmt.Errorf("offline")
			}
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	refs := buildModelRefs("shared", registry)
	if len(refs) != 2 || refs[1] != "two/shared" {
		t.Fatalf("fallback refs = %v", refs)
	}
	if _, err := adapter.Complete(context.Background(), refs[0], nil, nil); err == nil {
		t.Fatal("primary outage not simulated")
	}
	if _, err := adapter.Complete(context.Background(), refs[1], nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 || called[0] != "one" || called[1] != "two" {
		t.Fatalf("outage routing = %v", called)
	}
}

type roleEmbedder struct{ calls int }

func (e *roleEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1}, nil
}

func (e *roleEmbedder) EmbedDocuments(
	context.Context,
	[]embed.EmbeddingRequest,
) ([][]float32, error) {
	e.calls++
	return [][]float32{{1}}, nil
}

func TestEmbeddingPortRefusesModelWithoutEmbed(t *testing.T) {
	models := map[string][]modelcatalog.Model{
		"api": {{ProviderModel: "vector", Capabilities: modelcatalog.Capabilities{Embed: true}}},
	}
	registry := testRegistry(t, models)
	inner := &roleEmbedder{}
	port := &guardedEmbedder{
		inner:    inner,
		registry: registry,
		binding:  modelcatalog.RoleBinding{Role: "embed", Provider: "api", Model: "vector"},
	}
	if _, err := port.EmbedQuery(context.Background(), "context"); err != nil {
		t.Fatal(err)
	}
	models["api"][0].Capabilities.Embed = false
	registry.Refresh(context.Background())
	if _, err := port.EmbedQuery(context.Background(), "context"); err == nil {
		t.Fatal("missing embed capability accepted")
	}
	if _, err := port.EmbedDocuments(context.Background(), []embed.EmbeddingRequest{{Text: "context"}}); err == nil {
		t.Fatal("document embedding bypassed guard")
	}
	if inner.calls != 1 {
		t.Fatalf("embedding calls = %d", inner.calls)
	}
}
