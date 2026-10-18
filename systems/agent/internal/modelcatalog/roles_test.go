package modelcatalog

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRequireChatRefusesTheDecisionSelection(t *testing.T) {
	r := New(nil, nil, 0, 0)
	m := Model{
		ProviderName:  "api",
		ProviderModel: "both",
		Ref:           "both",
		Capabilities:  Capabilities{Chat: true, Decision: true},
	}
	if err := r.RequireChat("primary", m); err != nil {
		t.Fatal(err)
	}
	r.SetDecision(RoleSelection{Provider: "api", Model: "both"})
	err := r.RequireChat("primary", m)
	if err == nil || !strings.Contains(err.Error(), "decision model") {
		t.Fatalf("chat dispatch accepted the decision selection: %v", err)
	}
	// Another provider's same-named model is a different selection target.
	other := Model{
		ProviderName:  "second",
		ProviderModel: "both",
		Ref:           "both",
		Capabilities:  Capabilities{Chat: true},
	}
	if err := r.RequireChat("primary", other); err != nil {
		t.Fatal(err)
	}
	// Clearing the selection lifts the refusal.
	r.SetRoleSelections(RoleSelection{}, RoleSelection{})
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

func TestRoleSelectionsRoundTrip(t *testing.T) {
	r := New(nil, nil, 0, 0)
	if _, ok := r.Decision(); ok {
		t.Fatal("decision selection set on first run")
	}
	if _, ok := r.Embed(); ok {
		t.Fatal("embed selection set on first run")
	}
	r.SetRoleSelections(
		RoleSelection{Provider: "api", Model: "clef"},
		RoleSelection{Provider: "local", Model: "qwen3-embedding-0.6b"},
	)
	decision, ok := r.Decision()
	if !ok || decision.Provider != "api" || decision.Model != "clef" {
		t.Fatalf("Decision() = %+v, %v", decision, ok)
	}
	embed, ok := r.Embed()
	if !ok || embed.Provider != "local" || embed.Model != "qwen3-embedding-0.6b" {
		t.Fatalf("Embed() = %+v, %v", embed, ok)
	}
	r.SetDecision(RoleSelection{Provider: "api", Model: "clef-flash"})
	decision, _ = r.Decision()
	if decision.Model != "clef-flash" {
		t.Fatalf("SetDecision() = %+v", decision)
	}
	if embed, ok := r.Embed(); !ok || embed.Model != "qwen3-embedding-0.6b" {
		t.Fatalf("setting decision disturbed embed: %+v, %v", embed, ok)
	}
	r.SetEmbed(RoleSelection{})
	if _, ok := r.Embed(); ok {
		t.Fatal("empty embed selection did not clear the slot")
	}
	if _, ok := r.Decision(); !ok {
		t.Fatal("clearing embed disturbed decision")
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
