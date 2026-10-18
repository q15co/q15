package app

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

func TestFallbackDispatchReportsConfiguredRole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caps   modelcatalog.Capabilities
		reason string
		want   string
	}{
		{"missing capability", modelcatalog.Capabilities{Text: true}, "", "missing capability chat"},
		{"dual bind", modelcatalog.Capabilities{Chat: true, Decision: true}, "", "requires allow_dual_bind"},
		{"missing partitions", modelcatalog.Capabilities{Chat: true, Decision: true}, "separate quotas", "requires separate role rate and budget limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := testRegistry(
				t,
				map[string][]modelcatalog.Model{
					"api": {{ProviderModel: "spare", Capabilities: tc.caps}},
				},
			)
			registry.SetRoleBindings([]modelcatalog.RoleBinding{
				{Role: "fallbacks[0]", Provider: "api", Model: "spare"},
				{Role: "decision", Provider: "api", Model: "spare", AllowDualBind: tc.reason},
			})
			builds := 0
			adapter, err := newModelAdapterWithFactory(
				registry,
				nil,
				func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) {
					builds++
					return &fakeModelClient{}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			assertRole := func(err string) {
				t.Helper()
				if !strings.Contains(err, "role fallbacks[0]") || !strings.Contains(err, "spare") ||
					!strings.Contains(err, tc.want) ||
					strings.Contains(err, "role primary") {
					t.Fatalf("fallback error = %q", err)
				}
			}
			if _, err := adapter.Complete(context.Background(), "api/spare", nil, nil); err == nil {
				t.Fatal("invalid fallback dispatched")
			} else {
				assertRole(err.Error())
			}
			plan, err := adapter.Plan([]string{"api/spare"}, modelselection.Requirements{})
			if err != nil || len(plan.EligibleRefs) != 0 || len(plan.Skipped) != 1 {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
			assertRole(plan.Skipped[0].Reason)
			if builds != 0 {
				t.Fatal("invalid fallback reached the provider factory")
			}
		})
	}
}

func TestFallbackDispatchUsesItsRolePartition(t *testing.T) {
	registry := testRegistry(
		t,
		map[string][]modelcatalog.Model{
			"api": {{ProviderModel: "shared", Capabilities: modelcatalog.Capabilities{Chat: true}}},
		},
	)
	registry.SetRoleBindings([]modelcatalog.RoleBinding{
		{
			Role:                  "primary",
			Provider:              "api",
			Model:                 "shared",
			RequestsPerMinute:     1,
			RequestBytesPerMinute: 10000,
		},
		{
			Role:                  "fallbacks[0]",
			Provider:              "api",
			Model:                 "shared",
			RequestsPerMinute:     2,
			RequestBytesPerMinute: 10000,
		},
	})
	client := &fakeModelClient{}
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return client, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(context.Background(), "shared", nil, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := adapter.Complete(context.Background(), "api/shared", nil, nil); err != nil {
			t.Fatalf("fallback inherited the primary quota: %v", err)
		}
	}
	if _, err := adapter.Complete(context.Background(), "api/shared", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "role fallbacks[0]") {
		t.Fatalf("fallback rate limit = %v", err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("provider calls = %d, want 3", len(client.calls))
	}
}
