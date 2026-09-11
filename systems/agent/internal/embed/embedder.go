package embed

import (
	"context"
	"fmt"
	"strings"
)

// NewEmbedder constructs the Embedder selected by Settings.Provider. An empty
// provider falls back to DefaultProvider (Gemini) so existing configurations
// keep working unchanged.
func NewEmbedder(ctx context.Context, settings Settings) (Embedder, error) {
	switch normalizeProvider(settings.Provider) {
	case ProviderGemini:
		return NewGeminiEmbedder(
			ctx,
			settings.GeminiAPIKey,
			settings.Model,
			settings.Dimensions,
			settings.BatchSize,
		)
	case ProviderOpenAI:
		return NewOpenAIEmbedder(
			settings.BaseURL,
			settings.APIKey,
			settings.Model,
			settings.Dimensions,
			settings.BatchSize,
		)
	default:
		return nil, fmt.Errorf(
			"embedding provider %q is not supported",
			strings.TrimSpace(settings.Provider),
		)
	}
}

func normalizeProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return DefaultProvider
	}
	return provider
}
