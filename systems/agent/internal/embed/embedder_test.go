package embed

import (
	"context"
	"strings"
	"testing"
)

func TestNewEmbedderDefaultsToGemini(t *testing.T) {
	embedder, err := NewEmbedder(context.Background(), Settings{GeminiAPIKey: "key"})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	if _, ok := embedder.(*GeminiEmbedder); !ok {
		t.Fatalf("NewEmbedder type = %T, want *GeminiEmbedder", embedder)
	}
}

func TestNewEmbedderSelectsGeminiExplicitly(t *testing.T) {
	embedder, err := NewEmbedder(context.Background(), Settings{
		Provider:     ProviderGemini,
		GeminiAPIKey: "key",
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	if _, ok := embedder.(*GeminiEmbedder); !ok {
		t.Fatalf("NewEmbedder type = %T, want *GeminiEmbedder", embedder)
	}
}

func TestNewEmbedderSelectsOpenAI(t *testing.T) {
	embedder, err := NewEmbedder(context.Background(), Settings{
		Provider:   ProviderOpenAI,
		APIKey:     "key",
		Model:      "text-embedding-3-small",
		Dimensions: 1024,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	openaiEmbedder, ok := embedder.(*OpenAIEmbedder)
	if !ok {
		t.Fatalf("NewEmbedder type = %T, want *OpenAIEmbedder", embedder)
	}
	if openaiEmbedder.batchSize != openaiEmbedBatchSize {
		t.Fatalf(
			"default openai batch size = %d, want %d",
			openaiEmbedder.batchSize,
			openaiEmbedBatchSize,
		)
	}
	if openaiEmbedder.model != "text-embedding-3-small" {
		t.Fatalf("openai model = %q, want text-embedding-3-small", openaiEmbedder.model)
	}
	if openaiEmbedder.dimensions != 1024 {
		t.Fatalf("openai dimensions = %d, want 1024", openaiEmbedder.dimensions)
	}
}

func TestNewEmbedderSelectsOpenAIKeylessLocalMode(t *testing.T) {
	embedder, err := NewEmbedder(context.Background(), Settings{
		Provider:   ProviderOpenAI,
		BaseURL:    "http://localhost:8080/v1",
		Model:      "nomic-embed-text",
		Dimensions: 768,
		BatchSize:  64,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	openaiEmbedder, ok := embedder.(*OpenAIEmbedder)
	if !ok {
		t.Fatalf("NewEmbedder type = %T, want *OpenAIEmbedder", embedder)
	}
	if openaiEmbedder.batchSize != 64 {
		t.Fatalf("openai batch size = %d, want 64", openaiEmbedder.batchSize)
	}
	if openaiEmbedder.apiKey != "" {
		t.Fatalf("openai API key = %q, want empty", openaiEmbedder.apiKey)
	}
}

func TestNewEmbedderUnknownProvider(t *testing.T) {
	_, err := NewEmbedder(context.Background(), Settings{Provider: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), `embedding provider "bogus" is not supported`) {
		t.Fatalf("error = %q, want unknown provider message", err)
	}
}

func TestNewEmbedderOpenAIRequiresModelAndKey(t *testing.T) {
	if _, err := NewEmbedder(context.Background(), Settings{
		Provider:   ProviderOpenAI,
		APIKey:     "key",
		Dimensions: 1024,
	}); err == nil {
		t.Fatal("expected error for missing openai model")
	}
	if _, err := NewEmbedder(context.Background(), Settings{
		Provider:   ProviderOpenAI,
		Model:      "text-embedding-3-small",
		Dimensions: 1024,
	}); err == nil {
		t.Fatal("expected error for missing openai API key without base URL")
	}
}

// TestCurrentVectorVersionGeminiLegacyFormat pins the pre-provider stamp
// byte-for-byte: upgrading with provider=gemini (or unset) must not force a
// full re-embed of existing state.
func TestCurrentVectorVersionGeminiLegacyFormat(t *testing.T) {
	want := "dense:gemini-embedding-2:768;sparse:qdrant/bm25"
	for name, settings := range map[string]Settings{
		"empty provider":    {},
		"explicit gemini":   {Provider: ProviderGemini},
		"whitespace padded": {Provider: "  "},
	} {
		if got := currentVectorVersion(settings); got != want {
			t.Fatalf("%s: currentVectorVersion = %q, want %q", name, got, want)
		}
	}
	custom := Settings{Provider: ProviderGemini, Model: "gemini-embedding-1", Dimensions: 3072}
	if got, want := currentVectorVersion(custom),
		"dense:gemini-embedding-1:3072;sparse:qdrant/bm25"; got != want {
		t.Fatalf("currentVectorVersion = %q, want %q", got, want)
	}
}

func TestCurrentVectorVersionOpenAIFormat(t *testing.T) {
	settings := Settings{
		Provider:   ProviderOpenAI,
		Model:      "text-embedding-3-small",
		Dimensions: 1024,
	}
	want := "dense:openai:text-embedding-3-small:1024;sparse:qdrant/bm25"
	if got := currentVectorVersion(settings); got != want {
		t.Fatalf("currentVectorVersion = %q, want %q", got, want)
	}
}

func TestCurrentVectorVersionDistinctPerProviderModelDims(t *testing.T) {
	variants := []Settings{
		{},
		{
			Provider:   ProviderOpenAI,
			Model:      DefaultEmbeddingModel,
			Dimensions: DefaultEmbeddingDimensions,
		},
		{Model: "custom-model", Dimensions: 768},
		{Model: "custom-model", Dimensions: 1536},
		{Provider: ProviderOpenAI, Model: "text-embedding-3-small", Dimensions: 1024},
		{Provider: ProviderOpenAI, Model: "text-embedding-3-large", Dimensions: 1024},
	}
	seen := make(map[string]int, len(variants))
	for i, settings := range variants {
		stamp := currentVectorVersion(settings)
		if prev, ok := seen[stamp]; ok {
			t.Fatalf(
				"variants %d and %d both produce stamp %q; provider/model/dims must disambiguate",
				prev,
				i,
				stamp,
			)
		}
		seen[stamp] = i
	}
}
