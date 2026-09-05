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
// ProviderModel matching tries progressively less-specific CandidateKeys. The
// full provider ID wins when models.dev contains a versioned entry, while the
// tag-stripped fallback still lets deployment markers such as ":cloud" match
// an untagged catalog entry.
func Merge(base, enriched []Model) []Model {
	if len(base) == 0 {
		return nil
	}
	byKey := make(map[string]Model, len(enriched))
	for _, e := range enriched {
		keys := CandidateKeys(e.ProviderModel)
		if len(keys) > 0 {
			// Enrich emits the exact models.dev key as ProviderModel. Index only
			// that most-specific key so a versioned fallback cannot shadow a
			// distinct unversioned catalog entry.
			byKey[keys[0]] = e
		}
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

// CandidateKeys returns progressively less-specific, lowercased lookup keys
// for a provider model ID. It preserves version tags first, strips a trailing
// deployment suffix from the tag next, and strips the tag entirely only as a
// backward-compatible fallback.
//
// For example, "model:0731-cloud" yields:
//
//	["model:0731-cloud", "model:0731", "model"]
func CandidateKeys(providerModel string) []string {
	s := strings.ToLower(strings.TrimSpace(providerModel))
	if s == "" {
		return nil
	}

	keys := []string{s}
	base, tag, tagged := strings.Cut(s, ":")
	if !tagged || base == "" {
		return keys
	}

	if stripped := stripDeploymentSuffix(tag); stripped != "" && stripped != tag {
		keys = append(keys, base+":"+stripped)
	}
	if keys[len(keys)-1] != base {
		keys = append(keys, base)
	}
	return keys
}

// stripDeploymentSuffix removes hosting markers appended to an Ollama tag.
// The input is expected to be lowercased by the caller.
func stripDeploymentSuffix(tag string) string {
	for _, suffix := range []string{"-cloud", "-local"} {
		tag = strings.TrimSuffix(tag, suffix)
	}
	return tag
}
