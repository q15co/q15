package modelcatalog

import (
	"context"
	"errors"
	"strings"
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

func TestRegistry_SetProvidersSwapsTheDiscoveryTargets(t *testing.T) {
	cat := &fakeCatalog{models: map[string][]Model{
		"a": {{ProviderModel: "one"}},
		"b": {{ProviderModel: "two"}},
	}}
	reg := New([]Provider{{Name: "a", Type: "ollama"}}, cat, time.Hour, time.Second)
	reg.Refresh(context.Background())

	if err := reg.SetProviders(nil); err == nil {
		t.Error(
			"SetProviders(nil) should refuse an empty list: a running agent keeps something to discover",
		)
	}

	if err := reg.SetProviders([]Provider{{Name: "b", Type: "ollama"}}); err != nil {
		t.Fatalf("SetProviders: %v", err)
	}
	// The roster is whatever the last refresh found, until the next one.
	if !reg.ProviderHasModels("a") {
		t.Error("the roster should still hold the previous providers until the next refresh")
	}

	reg.Refresh(context.Background())
	if !reg.ProviderHasModels("b") {
		t.Error("the roster should follow the new provider list after a refresh")
	}
	if reg.ProviderHasModels("a") {
		t.Error("a provider that is no longer configured should not be in the roster")
	}
	if _, ok := reg.ProviderStatus("a"); ok {
		t.Error("a provider that is no longer configured should not report a status")
	}
}

func TestRegistry_ReloadStatusIsAbsentUntilAnAttempt(t *testing.T) {
	reg := New([]Provider{{Name: "a", Type: "ollama"}}, &fakeCatalog{}, time.Hour, time.Second)

	if _, ok := reg.ReloadStatus(); ok {
		t.Error("no reload has been attempted, so there should be no status")
	}

	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	reg.SetReloadStatus(ReloadStatus{CheckedAt: at, Err: "decode config: nope"})
	status, ok := reg.ReloadStatus()
	if !ok || status.Applied || status.Err != "decode config: nope" ||
		!status.CheckedAt.Equal(at) {
		t.Fatalf("reload status = %+v (found=%v), want the recorded refusal", status, ok)
	}

	reg.SetReloadStatus(ReloadStatus{CheckedAt: at.Add(time.Minute), Applied: true})
	status, _ = reg.ReloadStatus()
	if !status.Applied || status.Err != "" {
		t.Fatalf("reload status = %+v, want the applied reload", status)
	}
}

func TestRegistry_ProviderStatusSeparatesFailureFromEmptyRoster(t *testing.T) {
	cat := &fakeCatalog{
		models: map[string][]Model{
			"ok":    {{ProviderModel: "good"}},
			"quiet": {},
		},
		errs: map[string]error{
			"down": errors.New("openai models: unexpected status 404 Not Found"),
		},
	}
	reg := New(
		[]Provider{
			{Name: "ok", Type: "ollama"},
			{Name: "quiet", Type: "ollama"},
			{Name: "down", Type: "openai-compatible"},
		},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())

	// A provider with models says so and carries no reason.
	if ok, found := reg.ProviderStatus("ok"); !found || ok.Models != 1 || ok.Reason() != "" {
		t.Fatalf("status(ok) = %+v (found=%v), want 1 model and no reason", ok, found)
	}

	// A provider that answered with an empty roster and a provider whose fetch
	// failed must not read the same: the roster cannot tell them apart, and the
	// operator has to be able to.
	quiet, found := reg.ProviderStatus("quiet")
	if !found {
		t.Fatal("status(quiet) not recorded")
	}
	if !strings.Contains(quiet.Reason(), "listed no models") {
		t.Errorf("reason(quiet) = %q, want it to say the provider listed no models", quiet.Reason())
	}

	down, found := reg.ProviderStatus("down")
	if !found {
		t.Fatal("status(down) not recorded")
	}
	if down.Models != 0 {
		t.Errorf("status(down).Models = %d, want 0", down.Models)
	}
	if !strings.Contains(down.Reason(), "404 Not Found") {
		t.Errorf("reason(down) = %q, want the discovery error", down.Reason())
	}
	if quiet.Reason() == down.Reason() {
		t.Error("a failed fetch and an empty roster read identically")
	}
	if down.CheckedAt.IsZero() {
		t.Error("status(down).CheckedAt is zero, want the time of the attempt")
	}

	if _, found := reg.ProviderStatus("never-configured"); found {
		t.Error("status for an unconfigured provider should not be found")
	}
	if len(reg.Snapshot()) != 1 {
		t.Errorf(
			"snapshot len = %d, want 1: only the reachable provider contributes",
			len(reg.Snapshot()),
		)
	}
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
		{"kimi-k2.7-code:local", "kimi-k2.7-code"},
		{"deepseek-v4-flash:0731", "deepseek-v4-flash-0731"},
		{"deepseek-v4-flash:0731-cloud", "deepseek-v4-flash-0731"},
		{"gpt-oss:20b", "gpt-oss-20b"},
		{"gemma4:31b", "gemma4-31b"},
		{"mistral-large-3:675b", "mistral-large-3-675b"},
		{"a:b:c", "a-b-c"},
		{"org/gpt-4o", "org-gpt-4o"},
		{"plain", "plain"},
		{"  Spaced  ", "spaced"},
	}
	for _, tc := range tests {
		if got := deriveRef(tc.in); got != tc.want {
			t.Errorf("deriveRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRegistry_VersionedModelsHaveDistinctReachableRefs(t *testing.T) {
	cat := &fakeCatalog{models: map[string][]Model{
		"ollama-cloud": {
			{ProviderModel: "deepseek-v4-flash"},
			{ProviderModel: "deepseek-v4-flash:0731"},
		},
	}}
	reg := New(
		[]Provider{{Name: "ollama-cloud", Type: "ollama"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())

	oldModel, ok := reg.Lookup("ollama-cloud", "deepseek-v4-flash")
	if !ok {
		t.Fatal("unversioned model ref is not reachable")
	}
	if oldModel.ProviderModel != "deepseek-v4-flash" {
		t.Errorf("unversioned ref resolved to %q", oldModel.ProviderModel)
	}
	newModel, ok := reg.Lookup("ollama-cloud", "deepseek-v4-flash-0731")
	if !ok {
		t.Fatal("versioned model ref is not reachable")
	}
	if newModel.ProviderModel != "deepseek-v4-flash:0731" {
		t.Errorf("versioned ref resolved to %q", newModel.ProviderModel)
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
