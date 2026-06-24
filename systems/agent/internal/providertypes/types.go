// Package providertypes centralizes q15 model-provider type names and aliases.
package providertypes

import "strings"

const (
	// Ollama is the canonical provider type for Ollama-compatible APIs.
	Ollama = "ollama"
	// OpenAICompatible is the canonical provider type for OpenAI-compatible APIs.
	OpenAICompatible = "openai-compatible"
	// OpenAICodex is the canonical provider type for the OpenAI Codex adapter.
	OpenAICodex = "openai-codex"
)

// Normalize returns the canonical provider type string and whether it is supported.
func Normalize(providerType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case Ollama:
		return Ollama, true
	case "moonshot", OpenAICompatible, "openai_compatible":
		return OpenAICompatible, true
	case OpenAICodex, "openai_codex":
		return OpenAICodex, true
	default:
		return "", false
	}
}

// MustNormalize returns the canonical provider type string, or the empty string
// when providerType is unsupported. It is a convenience for legacy call sites
// that already treat empty as unsupported.
func MustNormalize(providerType string) string {
	providerType, ok := Normalize(providerType)
	if !ok {
		return ""
	}
	return providerType
}
