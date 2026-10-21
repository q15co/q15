package embedtools

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/embed"
)

// minUsefulExcerptBytes is the smallest excerpt worth reading for a long
// string payload value. Below this, truncation stops helping and the
// degradation ladder moves to dropping the payload instead.
const minUsefulExcerptBytes = 256

// boundedSearchOutput marshals search results and, when the output would
// exceed capBytes, degrades it instead of failing: long payload values are
// excerpted oldest-value-first with per-hit truncation disclosure, then
// trailing payloads drop (path/score-only hits), then trailing hits drop.
// The error path is reserved for results that cannot be bounded at all; it
// names sizes, never content.
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

	truncatedHit := false
	dropped := 0
	for {
		if output, ok := marshalBoundedResults(bounded, truncatedHit, dropped); ok &&
			len(output) <= capBytes {
			return output, nil
		}
		switch {
		case cutLongestPayloadString(bounded, &truncatedHit):
			// keep cutting
		case clearTrailingPayload(bounded):
			// keep dropping payloads
		case len(bounded) > 0:
			bounded = bounded[:len(bounded)-1]
			dropped++
		default:
			return "", errUnsatisfiableSearchOutput(capBytes)
		}
	}
}

// marshalBoundedResults renders the degraded payload. Truncation disclosures
// and the dropped-hit count appear only when they actually happened.
func marshalBoundedResults(
	bounded []embed.SearchResult,
	truncatedHit bool,
	dropped int,
) (string, bool) {
	body := map[string]any{"results": bounded}
	if truncatedHit {
		body["truncated"] = true
	}
	if dropped > 0 {
		body["dropped_hits"] = dropped
	}
	output, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", false
	}
	return string(output), true
}

// cutLongestPayloadString halves the longest still-reducible string value
// across all hit payloads, cloning the owning hit lazily so caller-held
// results are never mutated. Reduction stops at minUsefulExcerptBytes per
// value. Reports whether any value was reducible.
func cutLongestPayloadString(bounded []embed.SearchResult, truncatedHit *bool) bool {
	largestHit, largestKey, largestSize := -1, "", 0
	for i := range bounded {
		for key, value := range bounded[i].Payload {
			if text, ok := value.(string); ok && len(text) > minUsefulExcerptBytes &&
				len(text) > largestSize {
				largestHit, largestKey, largestSize = i, key, len(text)
			}
		}
	}
	if largestHit < 0 {
		return false
	}

	hit := &bounded[largestHit]
	cloned := make(map[string]any, len(hit.Payload)+2)
	for key, value := range hit.Payload {
		cloned[key] = value
	}
	original := cloned[largestKey].(string)
	target := max(largestSize/2, minUsefulExcerptBytes)
	cut := original[:target]
	// keep the excerpt rune-clean: a split multi-byte rune would poison the
	// excerpt with replacement characters
	for !utf8.ValidString(cut) && len(cut) > 0 {
		cut = cut[:len(cut)-1]
	}
	cloned[largestKey] = cut
	cloned["truncated"] = true
	cloned["omitted_bytes"] = existingOmitted(cloned) + (largestSize - len(cut))
	hit.Payload = cloned
	*truncatedHit = true
	return true
}

func existingOmitted(payload map[string]any) int {
	if omitted, ok := payload["omitted_bytes"].(int); ok {
		return omitted
	}
	return 0
}

// clearTrailingPayload drops the payload of the lowest-scored trailing hit,
// leaving its identity (collection/id/score) as the smallest useful record.
func clearTrailingPayload(bounded []embed.SearchResult) bool {
	for i := len(bounded) - 1; i >= 0; i-- {
		if bounded[i].Payload != nil {
			bounded[i].Payload = nil
			return true
		}
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
