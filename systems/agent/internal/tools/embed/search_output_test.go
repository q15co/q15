package embedtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/embed"
)

// parseSearchOutput decodes a successful embed_search output payload.
func parseSearchOutput(t *testing.T, output string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("bounded output is not valid JSON: %v", err)
	}
	if _, ok := decoded["results"].([]any); !ok {
		t.Fatalf("bounded output lost its results array: %v", decoded)
	}
	return decoded
}

func oversizedHit(collection, id string, score float32, text string) embed.SearchResult {
	return embed.SearchResult{
		Collection: collection,
		ID:         id,
		Score:      score,
		Payload: map[string]any{
			"text":      text,
			"file_path": "/memory/semantic/" + id + ".md",
			"section":   "Notes",
		},
	}
}

// TestSearchRunBoundsSingleOversizedHit covers the issue's core case: one hit
// larger than the whole output cap must return a bounded success (excerpt +
// source ref + truncation disclosure), not an error.
func TestSearchRunBoundsSingleOversizedHit(t *testing.T) {
	hit := oversizedHit(
		embed.CollectionSemantic,
		"synthetic-huge",
		0.97,
		strings.Repeat("huge ", 16*1024),
	)
	got, err := NewSearch(
		&fakeService{results: []embed.SearchResult{hit}},
	).Run(context.Background(), `{"query":"synthetic","collection":"semantic","limit":1}`)
	if err != nil {
		t.Fatalf("oversized single hit errored instead of degrading: %v", err)
	}
	if len(got) > maxSearchOutputBytes {
		t.Fatalf("bounded output = %d bytes, cap %d", len(got), maxSearchOutputBytes)
	}
	decoded := parseSearchOutput(t, got)
	results := decoded["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %d, want the one hit retained", len(results))
	}
	first := results[0].(map[string]any)
	if first["id"] != "synthetic-huge" || first["collection"] != embed.CollectionSemantic {
		t.Fatalf("hit identity lost: %v", first)
	}
	payload := first["payload"].(map[string]any)
	if payload["file_path"] != "/memory/semantic/synthetic-huge.md" {
		t.Fatalf("source reference lost: %v", payload)
	}
	text, ok := payload["text"].(string)
	if !ok || len(text) == 0 || len(text) > 14*1024 {
		t.Fatalf("excerpt missing or unbounded: %d bytes", len(text))
	}
	if payload["truncated"] != true {
		t.Fatalf("truncation not disclosed: %v", payload)
	}
	omitted, _ := payload["omitted_bytes"].(float64)
	if omitted <= 0 {
		t.Fatalf("omitted_bytes not disclosed: %v", payload)
	}
	if !strings.HasPrefix(text, "huge huge ") {
		t.Fatalf("excerpt lost the hit's leading content: %q", text[:min(len(text), 24)])
	}
}

// TestSearchRunDegradesHitsBeforeDropping: several huge hits must each shrink
// before any hit disappears; trailing hits drop last, disclosed by count.
func TestSearchRunDegradesHitsBeforeDropping(t *testing.T) {
	hits := []embed.SearchResult{
		oversizedHit(embed.CollectionSemantic, "first", 0.9, strings.Repeat("a", 100*1024)),
		oversizedHit(embed.CollectionSemantic, "second", 0.5, strings.Repeat("b", 100*1024)),
		oversizedHit(embed.CollectionSemantic, "third", 0.2, strings.Repeat("c", 100*1024)),
	}
	got, err := NewSearch(
		&fakeService{results: hits},
	).Run(context.Background(), `{"query":"synthetic","collection":"semantic","limit":3}`)
	if err != nil {
		t.Fatalf("multi-hit oversized errored instead of degrading: %v", err)
	}
	if len(got) > maxSearchOutputBytes {
		t.Fatalf("bounded output = %d bytes, cap %d", len(got), maxSearchOutputBytes)
	}
	decoded := parseSearchOutput(t, got)
	results := decoded["results"].([]any)
	if _, exists := results[0].(map[string]any)["payload"]; !exists {
		t.Fatal("top hit lost its payload instead of keeping an excerpt")
	}
	dropped, _ := decoded["dropped_hits"].(float64)
	if len(results)+int(dropped) != 3 {
		t.Fatalf(
			"returned %d hits + %.0f dropped, want the 3 served hits accounted for",
			len(results),
			dropped,
		)
	}
	// Trailing hits drop before leading ones: the best-scoring hit survives.
	if results[0].(map[string]any)["id"] != "first" {
		t.Fatalf("best hit was dropped before a weaker one: %v", results[0])
	}
}

// TestSearchOutputStaysUnderCapAdversarial: the marshalled result stays under
// the cap for pathological sizes, and always remains valid JSON.
func TestSearchOutputStaysUnderCapAdversarial(t *testing.T) {
	cases := map[string][]embed.SearchResult{
		"one 2MiB chunk": {oversizedHit(
			embed.CollectionLibrary, "twomb", 1, strings.Repeat("x", 2*1024*1024),
		)},
		"escaping-heavy": {oversizedHit(
			embed.CollectionSemantic, "escape", 1, strings.Repeat("\b\f", 60*1024),
		)},
		"unicode-heavy": {oversizedHit(
			embed.CollectionSemantic, "unicode", 1, strings.Repeat("✓", 60*1024),
		)},
		"fifty large hits": func() []embed.SearchResult {
			out := make([]embed.SearchResult, 50)
			for i := range out {
				out[i] = oversizedHit(
					embed.CollectionLibrary,
					"hit-"+string(rune('a'+i)),
					float32(50-i),
					strings.Repeat("y", 40*1024),
				)
			}
			return out
		}(),
	}
	for name, results := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NewSearch(
				&fakeService{results: results},
			).Run(context.Background(), `{"query":"synthetic","limit":50}`)
			if err != nil {
				t.Fatalf("adversarial sizes errored instead of degrading: %v", err)
			}
			if len(got) > maxSearchOutputBytes {
				t.Fatalf("output = %d bytes, cap %d", len(got), maxSearchOutputBytes)
			}
			parseSearchOutput(t, got)
		})
	}
}

// TestSearchRunUnrepresentableHitDisclosedAsDropped: a hit whose identity
// alone exceeds the cap degrades to an empty, disclosed result set — still a
// success, with the drop counted. The reserved error path (marshal failure or
// a skeleton that cannot fit any representation) never carries content.
func TestSearchRunUnrepresentableHitDisclosedAsDropped(t *testing.T) {
	hit := embed.SearchResult{
		Collection: embed.CollectionSemantic,
		ID:         strings.Repeat("z", 32*1024),
		Score:      1,
		Payload:    map[string]any{},
	}
	secret := "synthetic-payload-content-that-must-not-leak"
	hit.Payload["text"] = secret
	got, err := NewSearch(
		&fakeService{results: []embed.SearchResult{hit}},
	).Run(context.Background(), `{"query":"synthetic","collection":"semantic","limit":1}`)
	if err != nil {
		t.Fatalf("unrepresentable hit errored instead of degrading: %v", err)
	}
	if len(got) > maxSearchOutputBytes {
		t.Fatalf("output = %d bytes, cap %d", len(got), maxSearchOutputBytes)
	}
	decoded := parseSearchOutput(t, got)
	if results := decoded["results"].([]any); len(results) != 0 {
		t.Fatalf("results = %v, want the unrepresentable hit dropped", results)
	}
	if dropped, _ := decoded["dropped_hits"].(float64); dropped != 1 {
		t.Fatalf("dropped_hits = %v, want 1 disclosed", decoded["dropped_hits"])
	}
	if strings.Contains(got, secret) {
		t.Fatalf("output leaked payload content: %q", got)
	}
}

// TestSearchRunWithinCapByteIdentical pins the within-limit fast path: when
// output already fits it is marshalled exactly as before, with no disclosure
// keys and no payload edits.
func TestSearchRunWithinCapByteIdentical(t *testing.T) {
	results := []embed.SearchResult{
		oversizedHit(embed.CollectionSemantic, "small", 1, "short body"),
	}
	service := &fakeService{results: results}
	got, err := NewSearch(service).Run(
		context.Background(), `{"query":"synthetic","collection":"semantic","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.MarshalIndent(map[string]any{"results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(expected) {
		t.Fatal("within-limit result JSON changed")
	}
	if strings.Contains(got, "truncated") || strings.Contains(got, "dropped_hits") {
		t.Fatal("within-limit output gained disclosure keys")
	}
}

// TestSearchRunDoesNotMutateServiceResults: degradation works on copies; the
// service-returned slice and payloads are caller-owned.
func TestSearchRunDoesNotMutateServiceResults(t *testing.T) {
	original := oversizedHit(embed.CollectionSemantic, "owned", 1, strings.Repeat("w", 40*1024))
	service := &fakeService{results: []embed.SearchResult{original}}
	if _, err := NewSearch(service).Run(
		context.Background(), `{"query":"synthetic","limit":1}`); err != nil {
		t.Fatal(err)
	}
	payload := service.results[0].Payload
	if len(payload["text"].(string)) != 40*1024 {
		t.Fatalf("service result mutated: text now %d bytes", len(payload["text"].(string)))
	}
	if _, exists := payload["truncated"]; exists {
		t.Fatal("service result gained disclosure keys")
	}
}
