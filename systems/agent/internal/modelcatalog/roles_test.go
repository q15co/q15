package modelcatalog

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRequireChatAcceptsModelSupportingBothOperations(t *testing.T) {
	r := New(nil, nil, 0, 0)
	m := Model{
		ProviderName:  "api",
		ProviderModel: "both",
		Ref:           "both",
		Capabilities:  Capabilities{Chat: true, Decision: true},
	}
	r.SetDecision(RoleSelection{Provider: "api", Model: "both"})
	if err := r.RequireChat("primary", m); err != nil {
		t.Fatal(err)
	}
}

func TestRequireChatStillRequiresTheChatCapability(t *testing.T) {
	r := New(nil, nil, 0, 0)
	m := Model{
		ProviderName:  "api",
		ProviderModel: "decider",
		Ref:           "decider",
		Capabilities:  Capabilities{Decision: true, Text: true},
	}
	if err := r.RequireChat("primary", m); err == nil ||
		!strings.Contains(err.Error(), "missing capability chat") {
		t.Fatalf("missing chat capability accepted: %v", err)
	}
}

func TestDecisionSelectionRoundTrip(t *testing.T) {
	r := New(nil, nil, 0, 0)
	if _, ok := r.Decision(); ok {
		t.Fatal("decision selection set on first run")
	}
	r.SetDecision(RoleSelection{Provider: " api ", Model: " clef "})
	if sel, ok := r.Decision(); !ok || sel.Provider != "api" || sel.Model != "clef" {
		t.Fatalf("Decision() = %+v, %v", sel, ok)
	}
	r.SetDecision(RoleSelection{})
	if _, ok := r.Decision(); ok {
		t.Fatal("empty decision selection did not clear slot")
	}
}

// TestDecisionSelectionSurvivesRosterRefresh mirrors a boot-time ordering
// dependency: selections held on the registry outlive roster snapshots, and
// dispatch-time resolution re-checks the roster per call.
func TestDecisionSelectionSurvivesRosterRefresh(t *testing.T) {
	catalog := &roleTestCatalog{models: map[string][]Model{
		"api": {{
			ProviderModel: "clef",
			Capabilities:  Capabilities{Decision: true, Text: true},
		}},
	}}
	r := New(
		[]Provider{{Name: "api", Type: "ollama"}},
		catalog,
		time.Hour,
		time.Second,
	)
	r.Refresh(t.Context())
	r.SetDecision(RoleSelection{Provider: "api", Model: "clef"})
	r.Refresh(t.Context())
	if sel, ok := r.Decision(); !ok || sel.Model != "clef" {
		t.Fatalf("Decision() after refresh = %+v, %v", sel, ok)
	}
}

type roleTestCatalog struct {
	models map[string][]Model
}

func (c *roleTestCatalog) Discover(_ context.Context, p Provider) ([]Model, error) {
	return c.models[p.Name], nil
}
