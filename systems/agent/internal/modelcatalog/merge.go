package modelcatalog

import (
	"path"
	"strings"
)

// Merge joins base roster entries with enriched entries by ProviderModel.
//
// Enriched fields fill only zero-valued base fields — a roster entry is never
// dropped or overwritten wholesale. Capabilities are OR'd at the field level so
// that, for example, a roster model with Text and an enriched entry with
// ImageInput both survive.
//
// ProviderModel matching uses CandidateKeys: the full provider model ID is
// tried first (so versioned tags like "deepseek-v4-flash:0731" hit the
// correct models.dev entry), then deployment suffixes are stripped from the
// tag, then the tag is stripped entirely as a backward-compatible fallback.
//
// Enriched entries are keyed by their exact lowercased ProviderModel (as
// emitted by Enrich via modelFromModelsDev). Base entries are matched via
// CandidateKeys so a tagged base ID (e.g. "kimi-k2:cloud") finds an untagged
// enriched entry ("kimi-k2") through the fallback step.
func Merge(base, enriched []Model) []Model {
	if len(base) == 0 {
		return nil
	}
	byKey := make(map[string]Model, len(enriched))
	for _, e := range enriched {
		byKey[strings.ToLower(strings.TrimSpace(e.ProviderModel))] = e
	}

	out := make([]Model, len(base))
	for i, b := range base {
		for _, key := range CandidateKeys(b.ProviderModel) {
			if e, ok := byKey[key]; ok {
				b = mergeModel(b, e)
				break
			}
		}
		out[i] = b
	}
	return out
}

// mergeModel overlays enriched fields onto base where base is zero-valued.
func mergeModel(base, enriched Model) Model {
	if enriched.Name != "" && base.Name == base.ProviderModel {
		base.Name = enriched.Name
	}
	base.Capabilities = mergeCapabilities(base.Capabilities, enriched.Capabilities)
	if base.CostTier == "" {
		base.CostTier = enriched.CostTier
	}
	if base.CostPerMTokIn == 0 {
		base.CostPerMTokIn = enriched.CostPerMTokIn
	}
	if base.CostPerMTokOut == 0 {
		base.CostPerMTokOut = enriched.CostPerMTokOut
	}
	if base.MaxContextTokens == 0 {
		base.MaxContextTokens = enriched.MaxContextTokens
	}
	if base.MaxOutputTokens == 0 {
		base.MaxOutputTokens = enriched.MaxOutputTokens
	}
	if len(base.BenchmarkScores) == 0 && len(enriched.BenchmarkScores) > 0 {
		base.BenchmarkScores = enriched.BenchmarkScores
	}
	// ParameterCount: base (Ollama roster) wins if non-zero; else enriched.
	if base.ParameterCount == 0 {
		base.ParameterCount = enriched.ParameterCount
	}
	// ReleaseDate: enriched fills when base is zero (models.dev is the source).
	if base.ReleaseDate.IsZero() {
		base.ReleaseDate = enriched.ReleaseDate
	}
	// VideoInput and StructuredOutput are OR'd like capability flags.
	if enriched.VideoInput {
		base.VideoInput = true
	}
	if enriched.StructuredOutput {
		base.StructuredOutput = true
	}
	return base
}

// mergeCapabilities ORs enriched capability flags into base so enrichment can
// add capabilities (e.g. ImageInput from models.dev) without clearing any the
// roster already reported.
func mergeCapabilities(base, enriched Capabilities) Capabilities {
	if enriched.Text {
		base.Text = true
	}
	if enriched.ImageInput {
		base.ImageInput = true
	}
	if enriched.AudioInput {
		base.AudioInput = true
	}
	if enriched.ToolCalling {
		base.ToolCalling = true
	}
	if enriched.Reasoning {
		base.Reasoning = true
	}
	return base
}

// ApplyFilters keeps models whose ProviderModel matches at least one Include
// glob (when Include is non-empty) and matches no Exclude glob. Patterns use
// path.Match syntax. Invalid patterns are ignored (treated as non-matching).
func ApplyFilters(models []Model, include, exclude []string) []Model {
	if len(models) == 0 {
		return nil
	}
	if len(include) == 0 && len(exclude) == 0 {
		out := make([]Model, len(models))
		copy(out, models)
		return out
	}

	out := make([]Model, 0, len(models))
	for _, m := range models {
		if len(include) > 0 && !matchesAnyGlob(m.ProviderModel, include) {
			continue
		}
		if matchesAnyGlob(m.ProviderModel, exclude) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// matchesAnyGlob reports whether s matches any of the glob patterns. Invalid
// patterns are skipped (treated as non-matching).
func matchesAnyGlob(s string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if ok, err := path.Match(pattern, s); err == nil && ok {
			return true
		}
	}
	return false
}

// ModelKey normalizes a ProviderModel for join matching by stripping the
// Ollama ":tag" suffix and lowercasing. "kimi-k2.7-code:cloud" and
// "Kimi-K2.7-Code" both become "kimi-k2.7-code".
func ModelKey(providerModel string) string {
	s := strings.TrimSpace(providerModel)
	if idx := strings.Index(s, ":"); idx > 0 {
		s = s[:idx]
	}
	return strings.ToLower(s)
}

// CandidateKeys returns progressively less-specific lookup keys for a provider
// model ID, so versioned tags (e.g. "deepseek-v4-flash:0731") match the
// correct models.dev entry before falling back to the stripped base name
// ("deepseek-v4-flash"). This handles three real-world patterns:
//
//  1. Full ID as-is — the Ollama Cloud API returns clean IDs like
//     "deepseek-v4-flash:0731" that match models.dev keys directly.
//  2. Deployment suffix stripped — if a provider returns "model:0731-cloud",
//     the "-cloud" suffix is stripped to yield "model:0731".
//  3. Tag stripped entirely — backward-compatible fallback for unversioned
//     models.dev entries (e.g. "model:cloud" → "model").
//
// All keys are lowercased. Deduplication guarantees no repeated candidates.
func CandidateKeys(providerModel string) []string {
	s := strings.ToLower(strings.TrimSpace(providerModel))
	if s == "" {
		return nil
	}

	var out []string
	seen := make(map[string]bool)
	add := func(k string) {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}

	// 1. Full provider model as-is.
	add(s)

	if idx := strings.Index(s, ":"); idx > 0 {
		base := s[:idx]
		tag := s[idx+1:]

		// 2. Strip deployment suffixes from the tag (e.g. "0731-cloud" → "0731").
		stripped := stripDeploymentSuffix(tag)
		if stripped != "" && stripped != tag {
			add(base + ":" + stripped)
		}

		// 3. Strip the entire tag (backward compat, bare deployment markers).
		add(base)
	}

	return out
}
