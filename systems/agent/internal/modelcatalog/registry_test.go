package modelcatalog

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCatalog struct {
	models map[string][]Model
	errs   map[string]error
}

func (f *fakeCatalog) Discover(_ context.Context, p Provider) ([]Model, error) {
	if err, ok := f.errs[p.Name]; ok {
		return nil, err
	}
	return f.models[p.Name], nil
}

// callCountingCatalog wraps fakeCatalog to count how many times Discover is
// invoked. It is used to verify that Run does not refresh more than expected.
type callCountingCatalog struct {
	fakeCatalog
	calls int64
}

func (c *callCountingCatalog) Discover(ctx context.Context, p Provider) ([]Model, error) {
	atomic.AddInt64(&c.calls, 1)
	return c.fakeCatalog.Discover(ctx, p)
}

func TestRegistry_SnapshotReflectsSuccesses(t *testing.T) {
	cat := &fakeCatalog{models: map[string][]Model{
		"a": {
			{ProviderModel: "kimi:cloud", Capabilities: Capabilities{Text: true}},
			{ProviderModel: "glm"},
		},
		"b": {
			{ProviderModel: "gpt"},
		},
	}}
	reg := New(
		[]Provider{{Name: "a", Type: "ollama"}, {Name: "b", Type: "openai-compatible"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())

	snap := reg.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
}

func TestRegistry_ProviderErrorSkippedOthersPresent(t *testing.T) {
	cat := &fakeCatalog{
		models: map[string][]Model{
			"ok": {{ProviderModel: "good"}},
		},
		errs: map[string]error{
			"bad": errors.New("upstream 503"),
		},
	}
	reg := New(
		[]Provider{{Name: "ok", Type: "ollama"}, {Name: "bad", Type: "ollama"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())

	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1 (failed provider skipped)", len(snap))
	}
	if snap[0].ProviderModel != "good" {
		t.Fatalf("snapshot[0] = %q, want good", snap[0].ProviderModel)
	}
}

func TestRegistry_LookupByRef(t *testing.T) {
	cat := &fakeCatalog{models: map[string][]Model{
		"a": {
			{
				ProviderModel: "kimi-k2.7-code:cloud",
				Capabilities:  Capabilities{Text: true, ToolCalling: true},
			},
		},
	}}
	reg := New(
		[]Provider{{Name: "a", Type: "ollama"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())

	m, ok := reg.LookupByRef("kimi-k2.7-code")
	if !ok {
		t.Fatal("LookupByRef(kimi-k2.7-code) not found")
	}
	if m.ProviderModel != "kimi-k2.7-code:cloud" {
		t.Fatalf("ProviderModel = %q, want kimi-k2.7-code:cloud", m.ProviderModel)
	}
	if m.ProviderName != "a" {
		t.Fatalf("ProviderName = %q, want a", m.ProviderName)
	}
	if !m.Capabilities.ToolCalling {
		t.Fatal("ToolCalling should be true")
	}

	if _, ok := reg.LookupByRef("nonexistent"); ok {
		t.Fatal("LookupByRef(nonexistent) should not be found")
	}
}

func TestRegistry_RefreshReplacesSnapshotAtomically(t *testing.T) {
	cat := &fakeCatalog{models: map[string][]Model{
		"a": {{ProviderModel: "v1"}},
	}}
	reg := New([]Provider{{Name: "a", Type: "ollama"}}, cat, time.Hour, time.Second)
	reg.Refresh(context.Background())
	if len(reg.Snapshot()) != 1 {
		t.Fatalf("after first refresh: %d, want 1", len(reg.Snapshot()))
	}

	// Provider now returns a different model.
	cat.models["a"] = []Model{{ProviderModel: "v2"}}
	reg.Refresh(context.Background())
	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("after second refresh: %d, want 1", len(snap))
	}
	if snap[0].ProviderModel != "v2" {
		t.Fatalf("snapshot[0] = %q, want v2 (replaced)", snap[0].ProviderModel)
	}
}

func TestRegistry_DeriveRef(t *testing.T) {
	tests := []struct{ in, want string }{
		{"kimi-k2.7-code:cloud", "kimi-k2.7-code"},
		{"org/gpt-4o", "org-gpt-4o"},
		{"plain", "plain"},
		{"  spaced  ", "spaced"},
	}
	for _, tc := range tests {
		if got := deriveRef(tc.in); got != tc.want {
			t.Errorf("deriveRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRegistry_IsEmpty(t *testing.T) {
	reg := New(nil, nil, time.Hour, time.Second)
	if !reg.IsEmpty() {
		t.Fatal("empty registry should report IsEmpty")
	}
}

// TestRegistry_RunDoesNotRefreshInitially models the agent startup path: the
// caller refreshes synchronously to validate the roster, then starts Run in the
// background. Run must not perform its own initial refresh — it only ticks.
func TestRegistry_RunDoesNotRefreshInitially(t *testing.T) {
	cat := &callCountingCatalog{fakeCatalog: fakeCatalog{models: map[string][]Model{
		"a": {{ProviderModel: "m"}},
	}}}
	reg := New([]Provider{{Name: "a", Type: "ollama"}}, cat, time.Hour, time.Second)

	reg.Refresh(context.Background())
	if got := atomic.LoadInt64(&cat.calls); got != 1 {
		t.Fatalf("initial Refresh calls = %d, want 1", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = reg.Run(ctx)
	}()

	// Let Run start up; with an hour-long interval it should not tick.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := atomic.LoadInt64(&cat.calls); got != 1 {
		t.Fatalf("after Run: calls = %d, want 1 (Run must not refresh on its own)", got)
	}
}
