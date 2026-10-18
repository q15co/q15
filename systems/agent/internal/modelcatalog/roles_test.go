package modelcatalog

import "testing"

func TestDualBindPartitionsRateAndBudget(t *testing.T) {
	r := New(nil, nil, 0, 0)
	m := Model{
		ProviderName:  "api",
		ProviderModel: "both",
		Ref:           "both",
		Capabilities:  Capabilities{Chat: true, Decision: true},
	}
	r.SetRoleBindings([]RoleBinding{
		{
			Role:                  "primary",
			Provider:              "api",
			Model:                 "both",
			RequestsPerMinute:     1,
			RequestBytesPerMinute: 100,
		},
		{
			Role:                  "decision",
			Provider:              "api",
			Model:                 "both",
			AllowDualBind:         "separate quotas",
			RequestsPerMinute:     2,
			RequestBytesPerMinute: 10,
		},
	})
	if err := r.RequireChat(m); err != nil {
		t.Fatal(err)
	}
	if err := r.ReserveRole("primary", m, 90); err != nil {
		t.Fatal(err)
	}
	if err := r.ReserveRole("primary", m, 1); err == nil {
		t.Fatal("chat rate not enforced")
	}
	if err := r.ReserveRole("decision", m, 6); err != nil {
		t.Fatal("decision inherited chat quota", err)
	}
	if err := r.ReserveRole("decision", m, 5); err == nil {
		t.Fatal("decision byte budget not enforced")
	}
	if err := r.ReserveRole("decision", m, 4); err != nil {
		t.Fatal("rejected request consumed quota", err)
	}
}

func TestDecisionCapabilityDoesNotExcludeChat(t *testing.T) {
	r := New(nil, nil, 0, 0)
	if err := r.RequireChat(Model{ProviderModel: "both", Capabilities: Capabilities{Chat: true, Decision: true}}); err != nil {
		t.Fatal(err)
	}
	r.SetRoleBindings([]RoleBinding{{Role: "decision", Provider: "api", Model: "both"}})
	if err := r.RequireChat(Model{ProviderName: "api", ProviderModel: "both", Capabilities: Capabilities{Chat: true, Decision: true}}); err == nil {
		t.Fatal("explicit decision binding was ignored")
	}
}
