package models

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
)

func openTestStore(t *testing.T) (*selectionstore.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.json")
	store, err := selectionstore.Open(path)
	if err != nil {
		t.Fatalf("selectionstore.Open() error = %v", err)
	}
	return store, path
}

func reopenTestStore(t *testing.T, path string) *selectionstore.Store {
	t.Helper()
	reopened, err := selectionstore.Open(path)
	if err != nil {
		t.Fatalf("reopen store error = %v", err)
	}
	return reopened
}

type testCatalog struct {
	models map[string][]modelcatalog.Model
}

func (c testCatalog) Discover(
	_ context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return c.models[p.Name], nil
}

func testRegistry(t *testing.T) *modelcatalog.Registry {
	t.Helper()
	registry := modelcatalog.New(
		[]modelcatalog.Provider{
			{Name: "ollama-cloud", Type: "ollama", BaseURL: "https://ollama.com"},
			{Name: "openai", Type: "openai-compatible", BaseURL: "https://api.example.test/v1"},
		},
		testCatalog{models: map[string][]modelcatalog.Model{
			"ollama-cloud": {
				{
					ProviderModel: "kimi-k2.7-code:cloud",
					Name:          "Kimi K2.7 Code",
					Capabilities: modelcatalog.Capabilities{
						Text:        true,
						ToolCalling: true,
						Reasoning:   true,
					},
					MaxContextTokens: 262144,
					ParameterCount:   1042_000_000_000,
					ReleaseDate:      time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC),
					StructuredOutput: true,
				},
			},
			"openai": {
				{
					ProviderModel:    "gpt-5.5",
					Capabilities:     modelcatalog.Capabilities{Text: true, ToolCalling: true},
					MaxContextTokens: 1_000_000,
					CostPerMTokIn:    1.25,
					CostPerMTokOut:   10,
				},
			},
		}},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	return registry
}

func TestListProvidersReportsReachabilityAndCurrent(t *testing.T) {
	registry := testRegistry(t)
	selection := modelcatalog.NewSelection(registry, "ollama-cloud", "kimi-k2.7-code")
	out, err := NewListProviders(registry, selection).Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		`"current_provider": "ollama-cloud"`,
		`"name": "ollama-cloud"`,
		`"reachable": true`,
		`"current": true`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list_providers missing %q:\n%s", want, out)
		}
	}
}

type failingCatalog struct {
	models map[string][]modelcatalog.Model
	errs   map[string]error
}

func (c failingCatalog) Discover(
	_ context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	if err, ok := c.errs[p.Name]; ok {
		return nil, err
	}
	return c.models[p.Name], nil
}

// A provider that fails discovery and a provider that answers with an empty
// roster both leave the roster without models. list_providers has to say which
// one happened, or "reachable: false" is the only thing an operator ever sees.
func TestListProvidersReportsWhyAProviderHasNoModels(t *testing.T) {
	registry := modelcatalog.New(
		[]modelcatalog.Provider{
			{Name: "ollama-cloud", Type: "ollama", BaseURL: "https://ollama.com"},
			{
				Name:    "deepinfra",
				Type:    "openai-compatible",
				BaseURL: "https://api.deepinfra.com/v1/openai",
			},
			{Name: "quiet", Type: "openai-compatible", BaseURL: "https://quiet.example.test/v1"},
		},
		failingCatalog{
			models: map[string][]modelcatalog.Model{
				"ollama-cloud": {{ProviderModel: "kimi-k2.7-code:cloud"}},
			},
			errs: map[string]error{
				"deepinfra": errors.New("openai models: unexpected status 404 Not Found"),
			},
		},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())

	out, err := NewListProviders(registry, nil).Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var payload struct {
		Providers []struct {
			Name      string `json:"name"`
			Reachable bool   `json:"reachable"`
			Reason    string `json:"reason"`
			CheckedAt string `json:"checked_at"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("list_providers output is not JSON: %v\n%s", err, out)
	}

	reasons := map[string]string{}
	checkedAt := map[string]string{}
	for _, p := range payload.Providers {
		reasons[p.Name] = p.Reason
		checkedAt[p.Name] = p.CheckedAt
		if p.Name == "quiet" && p.Reachable {
			t.Error("a provider that listed no models should not be reported reachable")
		}
	}

	if reasons["ollama-cloud"] != "" {
		t.Errorf("a reachable provider should carry no reason, got %q", reasons["ollama-cloud"])
	}
	if !strings.Contains(reasons["deepinfra"], "discovery failed") ||
		!strings.Contains(reasons["deepinfra"], "404 Not Found") {
		t.Errorf("deepinfra reason = %q, want the discovery error", reasons["deepinfra"])
	}
	if !strings.Contains(reasons["quiet"], "listed no models") {
		t.Errorf(
			"quiet reason = %q, want it to say the provider listed no models",
			reasons["quiet"],
		)
	}
	if reasons["deepinfra"] == reasons["quiet"] {
		t.Error("a failed fetch and an empty roster read identically")
	}
	if checkedAt["deepinfra"] == "" {
		t.Error("an unreachable provider should report when discovery last ran")
	}
}

func TestListModelsFiltersByCapabilityAndContext(t *testing.T) {
	registry := testRegistry(t)
	selection := modelcatalog.NewSelection(registry, "ollama-cloud", "kimi-k2.7-code")
	out, err := NewListModels(registry, selection).Run(
		context.Background(),
		`{"capability":"tool_calling","min_context":500000}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out, "kimi-k2.7-code") {
		t.Fatalf("list_models included model below min_context:\n%s", out)
	}
	if !strings.Contains(out, `"ref": "gpt-5.5"`) {
		t.Fatalf("list_models missing gpt-5.5:\n%s", out)
	}
	if !strings.Contains(out, `"cost_per_mtok_in": 1.25`) {
		t.Fatalf("list_models missing cost metadata:\n%s", out)
	}
}

func TestSwitchModelValidatesAndUpdatesSelection(t *testing.T) {
	registry := testRegistry(t)
	selection := modelcatalog.NewSelection(registry, "ollama-cloud", "kimi-k2.7-code")
	store, _ := openTestStore(t)

	out, err := NewSwitchModel(registry, selection, store).Run(
		context.Background(),
		`{"provider":"openai","model":"gpt-5.5","reason":"need larger context"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	provider, model := selection.Current()
	if provider != "openai" || model != "gpt-5.5" {
		t.Fatalf("Current() = %q/%q, want openai/gpt-5.5", provider, model)
	}
	if got := store.Interactive(); got.Provider != "openai" || got.Model != "gpt-5.5" {
		t.Fatalf("persisted interactive = %+v, want openai/gpt-5.5", got)
	}
	if !strings.Contains(out, `"current": true`) || !strings.Contains(out, "need larger context") {
		t.Fatalf("switch_model output missing current/reason:\n%s", out)
	}

	if _, err := NewSwitchModel(registry, selection, store).Run(
		context.Background(),
		`{"provider":"openai","model":"kimi-k2.7-code"}`,
	); err == nil {
		t.Fatal("expected invalid provider/model pair to be rejected")
	}
}

func TestSwitchCognitionModelSetsClearsAndPersists(t *testing.T) {
	registry := testRegistry(t)
	store, path := openTestStore(t)
	tool := NewSwitchCognitionModel(
		registry,
		store,
		[]string{"working_memory.consolidate", "semantic_memory.extract"},
	)

	out, err := tool.Run(
		context.Background(),
		`{"job_type":"working_memory.consolidate","provider":"openai","model":"gpt-5.5","reason":"cheaper consolidation"}`,
	)
	if err != nil {
		t.Fatalf("Run(set) error = %v", err)
	}
	override, ok := store.Cognition("working_memory.consolidate")
	if !ok || override.Provider != "openai" || override.Model != "gpt-5.5" {
		t.Fatalf("persisted cognition override = %+v ok=%v", override, ok)
	}
	if !strings.Contains(out, "cheaper consolidation") {
		t.Fatalf("switch_cognition_model missing reason:\n%s", out)
	}

	reopened := reopenTestStore(t, path)
	if override, ok := reopened.Cognition("working_memory.consolidate"); !ok ||
		override.Model != "gpt-5.5" {
		t.Fatalf("cognition override did not survive reopen: %+v ok=%v", override, ok)
	}

	if _, err := NewSwitchCognitionModel(registry, reopened, []string{"working_memory.consolidate"}).Run(
		context.Background(),
		`{"job_type":"unregistered.job","provider":"openai","model":"gpt-5.5"}`,
	); err == nil {
		t.Fatal("expected unregistered job_type to be rejected")
	}

	if _, err := NewSwitchCognitionModel(registry, reopened, []string{"working_memory.consolidate"}).Run(
		context.Background(),
		`{"job_type":"working_memory.consolidate","action":"clear"}`,
	); err != nil {
		t.Fatalf("Run(clear) error = %v", err)
	}
	if _, ok := reopened.Cognition("working_memory.consolidate"); ok {
		t.Fatal("cognition override still present after clear")
	}
}

func TestSwitchCognitionModelRejectsUnknownAction(t *testing.T) {
	registry := testRegistry(t)
	store, _ := openTestStore(t)
	tool := NewSwitchCognitionModel(
		registry,
		store,
		[]string{"working_memory.consolidate"},
	)

	if _, err := tool.Run(
		context.Background(),
		`{"job_type":"working_memory.consolidate","action":"claer","provider":"openai","model":"gpt-5.5"}`,
	); err == nil {
		t.Fatal("expected unknown action to be rejected, not treated as set")
	}
	if overrides := store.CognitionSelections(); len(overrides) != 0 {
		t.Fatalf("unknown action persisted an override: %+v", overrides)
	}
}
