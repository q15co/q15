package embedtools

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/embed"
)

// minUsefulExcerptBytes is the smallest excerpt worth reading for a long
// string payload value. Below this, truncation stops helping and the
// degradation ladder moves to reducing the payload to its source reference.
const minUsefulExcerptBytes = 256

// referencePayloadKeys name the payload fields an agent needs to follow a hit
// up with read_file. They are never excerpted, and the ladder reduces a
// payload to exactly this reference set before it considers dropping a hit.
var referencePayloadKeys = []string{"file_path", "section", "source_id"}

// boundedSearchOutput marshals search results and, when the output would
// exceed capBytes, degrades it along a ladder instead of failing:
//
//  1. Excerpt oversized string payload values against the actual byte deficit
//     (largest first, deterministic order), marking each touched hit
//     truncated and adding the omitted byte count.
//  2. Reduce trailing payloads to their source reference (file_path et al.)
//     with the same disclosure.
//  3. Drop trailing hits, disclosed as dropped_hits.
//
// The error path is reserved for results that cannot be bounded at all; it
// names sizes, never content. Caller-held results are never mutated: any
// payload being written is JSON-clone-on-write first.
func boundedSearchOutput(results []embed.SearchResult, capBytes int) (string, error) {
	full, err := json.MarshalIndent(map[string]any{"results": results}, "", "  ")
	if err != nil {
		return "", err
	}
	if len(full) <= capBytes {
		return string(full), nil
	}

	bounded := make([]embed.SearchResult, len(results))
	copy(bounded, results)
	cloned := make([]bool, len(results))
	referenceOnly := make([]bool, len(results))

	disclosed := false
	dropped := 0
	for {
		output, err := marshalBoundedResults(bounded, disclosed, dropped)
		if err != nil {
			return "", err
		}
		if len(output) <= capBytes {
			return output, nil
		}
		deficit := len(output) - capBytes
		switch {
		case cutOversizedPayloadStrings(bounded, cloned, deficit, &disclosed):
			// keep cutting against the remaining deficit
		case reduceTrailingPayloadToReference(bounded, referenceOnly, &disclosed):
			// keep reducing to references
		case len(bounded) > 0:
			bounded = bounded[:len(bounded)-1]
			dropped++
		default:
			return "", errUnsatisfiableSearchOutput(capBytes)
		}
	}
}

// marshalBoundedResults renders the degraded payload. Omission disclosures
// appear only when they actually happened.
func marshalBoundedResults(
	bounded []embed.SearchResult,
	disclosed bool,
	dropped int,
) (string, error) {
	body := map[string]any{"results": bounded}
	if disclosed {
		body["truncated"] = true
	}
	if dropped > 0 {
		body["dropped_hits"] = dropped
	}
	output, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode bounded search output: %w", err)
	}
	return string(output), nil
}

type stringLeaf struct {
	path []any // string keys and int list indices addressing the leaf
	size int
}

func (l stringLeaf) pathString() string {
	return fmt.Sprint(l.path...)
}

// collectReducibleStrings walks a payload depth-first and lists string values
// still worth excerpting. Reference keys are exempt at any nesting depth.
func collectReducibleStrings(value any, path []any, out *[]stringLeaf) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if isReferenceKey(key) {
				continue
			}
			collectReducibleStrings(child, append(path, key), out)
		}
	case []any:
		for i, child := range node {
			collectReducibleStrings(child, append(path, i), out)
		}
	case string:
		if len(node) > minUsefulExcerptBytes {
			*out = append(*out, stringLeaf{path: append([]any(nil), path...), size: len(node)})
		}
	}
}

func isReferenceKey(key string) bool {
	for _, ref := range referencePayloadKeys {
		if key == ref {
			return true
		}
	}
	return false
}

// cutOversizedPayloadStrings fits excerpts to the actual byte deficit:
// candidates are ordered deterministically (largest first; ties break by hit
// index, then by key path), and each is cut by its share of the deficit
// rather than halved, so one pass usually settles a run. Every touched hit is
// JSON-clone-on-write, marked truncated, and gets the omitted byte count.
// Reports whether any value was reducible.
func cutOversizedPayloadStrings(
	bounded []embed.SearchResult,
	cloned []bool,
	deficit int,
	disclosed *bool,
) bool {
	type candidate struct {
		hit  int
		leaf stringLeaf
	}
	var candidates []candidate
	for i := range bounded {
		var leaves []stringLeaf
		collectReducibleStrings(bounded[i].Payload, nil, &leaves)
		for _, leaf := range leaves {
			candidates = append(candidates, candidate{hit: i, leaf: leaf})
		}
	}
	if len(candidates) == 0 {
		return false
	}
	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].leaf.size != candidates[b].leaf.size {
			return candidates[a].leaf.size > candidates[b].leaf.size
		}
		if candidates[a].hit != candidates[b].hit {
			return candidates[a].hit < candidates[b].hit
		}
		return candidates[a].leaf.pathString() < candidates[b].leaf.pathString()
	})

	remaining := deficit
	for _, cand := range candidates {
		if remaining <= 0 {
			break
		}
		reducible := cand.leaf.size - minUsefulExcerptBytes
		if reducible <= 0 {
			continue
		}
		take := min(remaining, reducible)
		target := cand.leaf.size - take
		original := cutPayloadString(&bounded[cand.hit], &cloned[cand.hit], cand.leaf.path, target)
		if original == "" {
			continue
		}
		remaining -= cand.leaf.size - len(original)
		*disclosed = true
	}
	return true
}

// cutPayloadString clones the hit payload if the caller still holds it
// (tracked in cloned, never inferred from payload content), then excerpts the
// string addressed by path to target bytes, kept rune-clean. Returns the
// excerpt, or "" when addressing failed.
func cutPayloadString(hit *embed.SearchResult, cloned *bool, path []any, target int) string {
	if hit.Payload == nil {
		return ""
	}
	if !*cloned {
		clonedBytes, err := json.Marshal(hit.Payload)
		if err != nil {
			return ""
		}
		var fresh map[string]any
		if err := json.Unmarshal(clonedBytes, &fresh); err != nil {
			return ""
		}
		hit.Payload = fresh
		*cloned = true
	}
	payload := hit.Payload

	var parent any = payload
	for _, step := range path[:len(path)-1] {
		switch node := parent.(type) {
		case map[string]any:
			parent = node[step.(string)]
		case []any:
			parent = node[step.(int)]
		default:
			return ""
		}
	}
	last := path[len(path)-1]
	var original string
	switch node := parent.(type) {
	case map[string]any:
		original, _ = node[last.(string)].(string)
		if original == "" {
			return ""
		}
		node[last.(string)] = excerptPrefix(original, target)
	case []any:
		original, _ = node[last.(int)].(string)
		if original == "" {
			return ""
		}
		node[last.(int)] = excerptPrefix(original, target)
	default:
		return ""
	}
	payload["truncated"] = true
	payload["omitted_bytes"] = existingOmitted(payload) + (len(original) - target)
	return excerptPrefix(original, target)
}

func excerptPrefix(original string, target int) string {
	cut := original[:target]
	for !utf8.ValidString(cut) && len(cut) > 0 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func existingOmitted(payload map[string]any) int {
	// Disclosure keys intentionally shadow colliding payload keys: a source
	// document's own truncated/omitted_bytes fields describe the source, and
	// once the tool excerpts, only the tool's accounting is meaningful.
	if omitted, ok := payload["omitted_bytes"].(float64); ok {
		return int(omitted)
	}
	if omitted, ok := payload["omitted_bytes"].(int); ok {
		return omitted
	}
	return 0
}

// reduceTrailingPayloadToReference strips the lowest-ranked unreduced payload
// down to its source reference keys, disclosing the elision exactly like a
// truncation. The reference is the agent's follow-up handle for read_file.
// Reduction is tracked positionally (referenceOnly), never by payload keys.
func reduceTrailingPayloadToReference(
	bounded []embed.SearchResult,
	referenceOnly []bool,
	disclosed *bool,
) bool {
	for i := len(bounded) - 1; i >= 0; i-- {
		payload := bounded[i].Payload
		if payload == nil || referenceOnly[i] {
			continue
		}
		var leaves []stringLeaf
		collectReducibleStrings(payload, nil, &leaves)
		nonReferenceKeys := 0
		for key := range payload {
			if isReferenceKey(key) || key == "truncated" || key == "omitted_bytes" {
				continue
			}
			nonReferenceKeys++
		}
		if len(leaves) == 0 && nonReferenceKeys == 0 {
			// Already nothing but a reference; mark positionally without
			// touching the payload.
			referenceOnly[i] = true
			continue
		}
		before, _ := json.Marshal(payload)
		reduced := map[string]any{}
		for _, key := range referencePayloadKeys {
			if value, exists := payload[key]; exists {
				reduced[key] = value
			}
		}
		after, _ := json.Marshal(reduced)
		reduced["truncated"] = true
		reduced["omitted_bytes"] = existingOmitted(payload) + len(before) - len(after)
		bounded[i].Payload = reduced
		referenceOnly[i] = true
		*disclosed = true
		return true
	}
	return false
}

func errUnsatisfiableSearchOutput(capBytes int) error {
	// Sizes and guidance only: a tool error must never carry result payloads
	// into logs.
	return fmt.Errorf(
		"search results exceed %d byte output limit even with every hit reduced to its reference; "+
			"narrow the query, reduce limit, or use paginated read_file",
		capBytes,
	)
}
