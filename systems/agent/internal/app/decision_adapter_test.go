package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	decisiontools "github.com/q15co/q15/systems/agent/internal/tools/decision"
)

type fakeDecisionClient struct{ requests []decision.Request }

func (c *fakeDecisionClient) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	if err := ctx.Err(); err != nil {
		return decision.Response{}, err
	}
	c.requests = append(c.requests, request)
	confidence := 0.75
	return decision.Response{
		Answers: map[string]decision.Answer{
			"check": {
				Type:          "choice",
				Choice:        "first",
				Probabilities: decision.Probabilities{"first": 0.9, "second": 0.1},
				Confidence:    &confidence,
			},
		},
	}, nil
}

func TestDecisionRoutingUsesDiscoveredMetadataAndReloadedProvider(t *testing.T) {
	catalog := &mockCatalog{models: map[string][]modelcatalog.Model{"gateway": {
		{ProviderModel: "chat", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
		{
			ProviderModel: "vendor/unfamiliar-model",
			Capabilities:  modelcatalog.Capabilities{Decision: true, Text: true},
		},
	}}}
	registry := modelcatalog.New(
		[]modelcatalog.Provider{
			{
				Name:    "gateway",
				Type:    "openai-compatible",
				BaseURL: "https://old.example/v1",
				APIKey:  "old-key",
			},
		},
		catalog,
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	registry.SetRoleSelections(
		modelcatalog.RoleSelection{
			Provider: "gateway",
			Model:    "vendor-unfamiliar-model",
		},
		modelcatalog.RoleSelection{},
	)
	client := &fakeDecisionClient{}
	var built []modelcatalog.Model
	adapter := newRoutedDecisionAdapter(
		registry,
		func(model modelcatalog.Model) (decision.Client, error) {
			built = append(built, model)
			return client, nil
		},
	)
	tool := decisiontools.NewCheck(adapter)
	args := `{"state":{"ticket":"context"},"question":"What next?","options":["first","second"]}`
	for range 2 {
		if _, err := tool.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	if len(built) != 1 || len(client.requests) != 2 ||
		client.requests[0].Model != "vendor/unfamiliar-model" {
		t.Fatalf("built = %+v, requests = %+v", built, client.requests)
	}
	if err := registry.ReplaceProviders(context.Background(), []modelcatalog.Provider{{Name: "gateway", Type: "openai-compatible", BaseURL: "https://new.example/custom/root", APIKey: "new-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 || built[1].ProviderBaseURL != "https://new.example/custom/root" ||
		built[1].ProviderAPIKey != "new-key" {
		t.Fatalf("client reused stale connection: %+v", built)
	}
	if err := registry.ReplaceProviders(context.Background(), []modelcatalog.Provider{{Name: "gateway", Type: "openai-compatible", BaseURL: "https://new.example/custom/root", APIKey: "new-key", DecisionPath: "/typed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if len(built) != 3 || built[2].ProviderDecisionPath != "/typed" {
		t.Fatalf("client reused stale decision path: %+v", built)
	}

}

func TestDecisionRoutingRejectsChatAndPropagatesCancellation(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{"provider": {
		{ProviderModel: "chat", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
		{
			ProviderModel: "decision",
			Capabilities:  modelcatalog.Capabilities{Decision: true, Text: true},
		},
	}})
	registry.SetRoleSelections(
		modelcatalog.RoleSelection{Provider: "provider", Model: "decision"},
		modelcatalog.RoleSelection{},
	)
	client := &fakeDecisionClient{}
	adapter := newRoutedDecisionAdapter(
		registry,
		func(modelcatalog.Model) (decision.Client, error) { return client, nil },
	)
	request := decision.Request{
		Model:     "chat",
		State:     "context",
		Questions: map[string]decision.Question{"check": decision.Noul("True?")},
	}
	if _, err := adapter.Check(context.Background(), request); err == nil {
		t.Fatal("chat model accepted for a decision")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request.Model = "decision"
	if _, err := adapter.Check(ctx, request); err != context.Canceled {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestDecisionContractVersion(t *testing.T) {
	for _, kind := range []string{"ollama", "openai-compatible", "systemone-compatible"} {
		client, err := defaultDecisionClientFactory(
			modelcatalog.Model{ProviderType: kind, ProviderBaseURL: "https://example.invalid/v1"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if got := client.ContractVersion(); got != "decision/v1" {
			t.Fatalf("%s contract = %q", kind, got)
		}
	}
}

// ContractVersion identifies the implemented decision port.
func (c *fakeDecisionClient) ContractVersion() string { return decision.ContractVersion }

func TestDecisionPortRefusesModelWithoutDecision(t *testing.T) {
	for _, ref := range []string{"", "native/model", "native-model"} {
		t.Run(ref, func(t *testing.T) {
			models := map[string][]modelcatalog.Model{"api": {{
				ProviderModel: "native/model",
				Capabilities:  modelcatalog.Capabilities{Chat: true},
			}}}
			registry := testRegistry(t, models)
			registry.SetRoleSelections(
				modelcatalog.RoleSelection{Provider: "api", Model: "native-model"},
				modelcatalog.RoleSelection{},
			)
			client := &fakeDecisionClient{}
			builds := 0
			adapter := newRoutedDecisionAdapter(
				registry,
				func(modelcatalog.Model) (decision.Client, error) {
					builds++
					return client, nil
				},
			)
			request := decision.Request{
				Model: ref, State: "private state",
				Questions: map[string]decision.Question{
					"check": decision.Choice(
						"Pick?",
						[]decision.Option{{Name: "first"}, {Name: "second"}},
					),
				},
			}
			refuse := func() {
				t.Helper()
				_, err := adapter.Check(context.Background(), request)
				if err == nil || !strings.Contains(err.Error(), "role decision") ||
					!strings.Contains(err.Error(), "native/model") ||
					!strings.Contains(err.Error(), "missing capability decision") ||
					strings.Contains(err.Error(), "private state") {
					t.Fatalf("decision refusal = %v", err)
				}
			}
			refuse()
			if builds != 0 || len(client.requests) != 0 {
				t.Fatal("missing decision capability reached the client factory or provider")
			}
			models["api"][0].Capabilities.Decision = true
			registry.Refresh(context.Background())
			if _, err := adapter.Check(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			models["api"][0].Capabilities.Decision = false
			registry.Refresh(context.Background())
			refuse()
			if builds != 1 || len(client.requests) != 1 {
				t.Fatal("cached client bypassed the live decision guard")
			}
			models["api"][0].Capabilities.Decision = true
			registry.Refresh(context.Background())
			if _, err := adapter.Check(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if builds != 1 || len(client.requests) != 2 {
				t.Fatalf("builds = %d, provider calls = %d", builds, len(client.requests))
			}
		})
	}
}
