package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// sampleModelsDevPayload is a compact api.json payload using the VERIFIED live
// schema (confirmed via curl https://models.dev/api.json on 2026-06-21):
//   - provider key "ollama-cloud" (NOT "ollama" — there is no generic ollama slug)
//   - capabilities are TOP-LEVEL booleans: tool_call, reasoning, attachment
//   - cost is a "cost" block with input/output (USD per MTok, NOT per-token strings)
//   - modalities.input/output arrays
//   - limit.context/output integers
//   - NO benchmarks field (models.dev publishes none)
func sampleModelsDevPayload() modelsDevFile {
	return modelsDevFile{
		"ollama-cloud": {
			Name: "Ollama Cloud",
			Models: map[string]modelsDevModel{
				"kimi-k2.7-code": {
					Name:        "kimi-k2.7-code",
					ReleaseDate: "2026-06-12",
					Limit:       modelsDevLimit{Context: 262144, Output: 262144},
					Modalities: modelsDevModalities{
						Input:  []string{"text", "image"},
						Output: []string{"text"},
					},
					ToolCall:         true,
					Reasoning:        true,
					Attachment:       true,
					StructuredOutput: true,
					// ollama-cloud models have NO cost block (0 of 43 as of 2026-06-21).
				},
				"minimax-m3": {
					Name:  "minimax-m3",
					Limit: modelsDevLimit{Context: 1000000, Output: 8192},
					Modalities: modelsDevModalities{
						Input:  []string{"text"},
						Output: []string{"text"},
					},
					ToolCall:  true,
					Reasoning: true,
				},
			},
		},
		// A provider with cost data (for testing the cost parsing path).
		"requesty": {
			Name: "Requesty",
			Models: map[string]modelsDevModel{
				"xai/grok-4": {
					Name:  "grok-4",
					Limit: modelsDevLimit{Context: 256000, Output: 8192},
					Modalities: modelsDevModalities{
						Input:  []string{"text"},
						Output: []string{"text"},
					},
					Cost:     modelsDevCost{Input: 3.0, Output: 15.0},
					ToolCall: true,
				},
			},
		},
	}
}

func writeModelsDevPayload(t *testing.T, payload modelsDevFile) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

// newCacheDir returns a fresh temp directory for the models.dev cache and
// registers cleanup.
func newCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(envCacheDir, dir)
	return dir
}

func TestModelsDevClient_FirstFetchThenCacheHit(t *testing.T) {
	newCacheDir(t)
	var fetchCount int32
	payload := writeModelsDevPayload(t, sampleModelsDevPayload())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := newModelsDevClient(server.Client())
	client.sourceURL = server.URL

	// First call fetches from the server.
	_, err := client.loadFile(context.Background())
	if err != nil {
		t.Fatalf("first loadFile: %v", err)
	}
	if got := atomic.LoadInt32(&fetchCount); got != 1 {
		t.Fatalf("expected 1 fetch after first call, got %d", got)
	}

	// Second call uses the in-memory cache.
	_, err = client.loadFile(context.Background())
	if err != nil {
		t.Fatalf("second loadFile: %v", err)
	}
	if got := atomic.LoadInt32(&fetchCount); got != 1 {
		t.Fatalf("expected 1 fetch after cached second call, got %d", got)
	}

	// Cache file was written to disk.
	if _, err := os.Stat(cacheFilePath()); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
}

func TestModelsDevClient_OfflineReusesStaleCache(t *testing.T) {
	newCacheDir(t)
	payload := writeModelsDevPayload(t, sampleModelsDevPayload())

	// Pre-populate a stale cache file (mtime in the past).
	cachePath := cacheFilePath()
	if err := os.WriteFile(cachePath, payload, 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}
	pastTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, pastTime, pastTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Server is down (we'll never start it). Use a URL pointing to an
	// unreachable port.
	client := newModelsDevClient(http.DefaultClient)
	client.sourceURL = "http://127.0.0.1:0/api.json" // unreachable

	file, err := client.loadFile(context.Background())
	if err != nil {
		t.Fatalf("expected stale-cache fallback, got error: %v", err)
	}
	if len(file) == 0 {
		t.Fatal("expected non-empty file from stale cache")
	}
	if _, ok := file["ollama-cloud"]; !ok {
		t.Fatal("stale cache missing ollama-cloud provider")
	}
}

func TestModelsDevClient_ExpiredTTLRefetches(t *testing.T) {
	newCacheDir(t)
	// Short TTL so "expired" is immediate.
	t.Setenv(envCacheTTL, "1")

	var fetchCount int32
	payload := writeModelsDevPayload(t, sampleModelsDevPayload())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fetchCount, 1)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	// Seed a stale cache file (older than the 1s TTL).
	cachePath := cacheFilePath()
	if err := os.WriteFile(cachePath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}
	pastTime := time.Now().Add(-1 * time.Minute)
	if err := os.Chtimes(cachePath, pastTime, pastTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	client := newModelsDevClient(server.Client())
	client.sourceURL = server.URL

	// Fresh client, in-memory cache is empty, disk cache is stale → fetch.
	file, err := client.loadFile(context.Background())
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if got := atomic.LoadInt32(&fetchCount); got != 1 {
		t.Fatalf("expected 1 fetch for expired cache, got %d", got)
	}
	if _, ok := file["ollama-cloud"]; !ok {
		t.Fatal("fetched file missing ollama-cloud provider")
	}
}

// TestModelsDevClient_EnrichRealOllamaCloudShape uses a REAL-SHAPE
// ollama-cloud payload (verified live 2026-06-21) to assert that the parser
// correctly reads top-level booleans and context limits.
func TestModelsDevClient_EnrichRealOllamaCloudShape(t *testing.T) {
	newCacheDir(t)
	payload := writeModelsDevPayload(t, sampleModelsDevPayload())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := newModelsDevClient(server.Client())
	client.sourceURL = server.URL

	// Roster entry has an Ollama :tag suffix (e.g. ":cloud") that models.dev
	// does not use in its model IDs.
	base := []Model{
		{
			ProviderModel: "kimi-k2.7-code:cloud",
			Name:          "kimi-k2.7-code:cloud",
			Capabilities:  Capabilities{Text: true},
			Source:        sourceOllama,
		},
	}

	enriched, err := client.Enrich(
		context.Background(),
		Provider{
			Type:    "ollama",
			Name:    "ollama-cloud",
			BaseURL: "https://ollama.com",
		},
		base,
	)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched entry, got %d", len(enriched))
	}
	e := enriched[0]
	// The :cloud tag is stripped before the join key lookup.
	if e.ProviderModel != "kimi-k2.7-code" {
		t.Errorf("enriched ProviderModel = %q, want %q", e.ProviderModel, "kimi-k2.7-code")
	}
	// Capabilities from top-level booleans + modalities.
	if !e.Capabilities.ToolCalling {
		t.Error("expected ToolCalling from top-level tool_call=true")
	}
	if !e.Capabilities.Reasoning {
		t.Error("expected Reasoning from top-level reasoning=true")
	}
	if !e.Capabilities.ImageInput {
		t.Error("expected ImageInput from attachment=true and modalities.input [text,image]")
	}
	// Context limit from limit.context.
	if e.MaxContextTokens != 262144 {
		t.Errorf("MaxContextTokens = %d, want 262144", e.MaxContextTokens)
	}
	// ollama-cloud has NO cost block → cost fields zero (unknown).
	if e.CostPerMTokIn != 0 {
		t.Errorf("CostPerMTokIn = %f, want 0 (ollama-cloud has no cost data)", e.CostPerMTokIn)
	}
	if e.CostPerMTokOut != 0 {
		t.Errorf("CostPerMTokOut = %f, want 0 (ollama-cloud has no cost data)", e.CostPerMTokOut)
	}
	if e.CostTier != "" {
		t.Errorf("CostTier = %q, want empty (no cost data)", e.CostTier)
	}
	// models.dev publishes no benchmarks → nil BenchmarkScores.
	if e.BenchmarkScores != nil {
		t.Errorf("BenchmarkScores = %v, want nil (models.dev has no benchmarks)", e.BenchmarkScores)
	}
	// New quality fields from models.dev.
	if !e.ReleaseDate.IsZero() {
		wantRD := "2026-06-12"
		if e.ReleaseDate.Format("2006-01-02") != wantRD {
			t.Errorf("ReleaseDate = %s, want %s", e.ReleaseDate.Format("2006-01-02"), wantRD)
		}
	} else {
		t.Error("ReleaseDate should not be zero")
	}
	if e.VideoInput {
		t.Error("VideoInput should be false (kimi has no video modality)")
	}
	if !e.StructuredOutput {
		t.Error("StructuredOutput should be true")
	}
}

// TestModelsDevClient_EnrichCostFromPaidProvider verifies the cost.input/output
// fields are read correctly (USD per MTok, no per-token conversion).
func TestModelsDevClient_EnrichCostFromPaidProvider(t *testing.T) {
	newCacheDir(t)
	payload := writeModelsDevPayload(t, sampleModelsDevPayload())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := newModelsDevClient(server.Client())
	client.sourceURL = server.URL

	base := []Model{
		{ProviderModel: "xai/grok-4", Name: "grok-4", Capabilities: Capabilities{Text: true}},
	}
	enriched, err := client.Enrich(
		context.Background(),
		Provider{Type: "openai-compatible", BaseURL: "https://api.requesty.ai/v1"},
		base,
	)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched entry, got %d", len(enriched))
	}
	e := enriched[0]
	// cost.input=3, cost.output=15 → 3.0 + 15.0 = 18.0/MTok total → "expensive"
	if e.CostPerMTokIn != 3.0 {
		t.Errorf("CostPerMTokIn = %f, want 3.0 (USD per MTok, no conversion)", e.CostPerMTokIn)
	}
	if e.CostPerMTokOut != 15.0 {
		t.Errorf("CostPerMTokOut = %f, want 15.0 (USD per MTok, no conversion)", e.CostPerMTokOut)
	}
	if e.CostTier != "expensive" { // 3 + 15 = 18 >= 5
		t.Errorf("CostTier = %q, want %q", e.CostTier, "expensive")
	}
}

func TestModelsDevClient_EnrichOfflineReturnsNil(t *testing.T) {
	newCacheDir(t)
	client := newModelsDevClient(http.DefaultClient)
	client.sourceURL = "http://127.0.0.1:0/api.json" // unreachable, no cache

	base := []Model{{ProviderModel: "kimi-k2.7-code", Source: sourceOllama}}
	enriched, err := client.Enrich(
		context.Background(),
		Provider{Type: "ollama", Name: "ollama-cloud", BaseURL: "https://ollama.com"},
		base,
	)
	if err != nil {
		t.Fatalf("Enrich should not return error when offline: %v", err)
	}
	if enriched != nil {
		t.Fatalf("expected nil enriched when offline, got %d entries", len(enriched))
	}
}

func TestModelFromModelsDev_TextFallbackWhenNoModalities(t *testing.T) {
	m := modelFromModelsDev("x", modelsDevModel{})
	if !m.Capabilities.Text {
		t.Error("expected Text fallback when no modalities recorded")
	}
}

func TestModelFromModelsDev_TopLevelBooleans(t *testing.T) {
	m := modelFromModelsDev("test", modelsDevModel{
		ToolCall:   true,
		Reasoning:  true,
		Attachment: true,
		Modalities: modelsDevModalities{Input: []string{"text"}},
	})
	if !m.Capabilities.ToolCalling {
		t.Error("expected ToolCalling from top-level tool_call")
	}
	if !m.Capabilities.Reasoning {
		t.Error("expected Reasoning from top-level reasoning")
	}
	if !m.Capabilities.ImageInput {
		t.Error("expected ImageInput from attachment=true")
	}
}

func TestDeriveCostTier(t *testing.T) {
	tests := []struct {
		in, out float64
		want    string
	}{
		{0.5, 0.4, "cheap"},     // 0.9 < 1
		{2.0, 2.5, "standard"},  // 4.5 < 5
		{3.0, 3.0, "expensive"}, // 6.0 >= 5
		{0, 0, ""},              // unknown
	}
	for _, tc := range tests {
		got := deriveCostTier(tc.in, tc.out)
		if got != tc.want {
			t.Errorf("deriveCostTier(%f,%f) = %q, want %q", tc.in, tc.out, got, tc.want)
		}
	}
}

func TestProviderSlug(t *testing.T) {
	tests := []struct {
		name string
		p    Provider
		want string
	}{
		{
			"ollama cloud by base_url",
			Provider{Type: "ollama", Name: "ollama-cloud", BaseURL: "https://ollama.com"},
			"ollama-cloud",
		},
		{
			"ollama cloud by name only",
			Provider{Type: "ollama", Name: "ollama-cloud", BaseURL: ""},
			"ollama-cloud",
		},
		{
			"local ollama no slug",
			Provider{Type: "ollama", Name: "local-ollama", BaseURL: "http://localhost:11434"},
			"",
		},
		{"codex", Provider{Type: "openai-codex"}, "openai"},
		{
			"openai-compatible moonshot",
			Provider{Type: "openai-compatible", BaseURL: "https://api.moonshot.ai/v1"},
			"moonshotai",
		},
		{
			"openai-compatible z.ai",
			Provider{Type: "openai-compatible", BaseURL: "https://api.z.ai/v1"},
			"zai",
		},
		{
			"openai-compatible openai",
			Provider{Type: "openai-compatible", BaseURL: "https://api.openai.com"},
			"openai",
		},
		{
			"openai-compatible unknown host no slug",
			Provider{Type: "openai-compatible", BaseURL: "https://internal.corp/v1"},
			"",
		},
		{"unknown provider type no slug", Provider{Type: "unknown-type", Name: "MyProvider"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := providerSlug(tc.p)
			if got != tc.want {
				t.Errorf("providerSlug() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseModelsDevFile_Empty(t *testing.T) {
	file, err := parseModelsDevFile(nil)
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if file != nil {
		t.Fatalf("expected nil for empty input, got %v", file)
	}
}

func TestReadCache_MissingFile(t *testing.T) {
	fresh, data := readCache("/nonexistent/path/cache.json", time.Hour)
	if fresh {
		t.Error("expected not fresh for missing file")
	}
	if data != nil {
		t.Errorf("expected nil data for missing file, got %d bytes", len(data))
	}
}

func TestWriteCache_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cache.json")
	data := []byte(`{"x":1}`)
	if err := writeCache(path, data); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !reflect.DeepEqual(got, data) {
		t.Errorf("data mismatch: got %q, want %q", got, data)
	}
}
