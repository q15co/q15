package embed

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	geminiEmbedBatchSize   = 32
	geminiEmbedMaxAttempts = 6
)

// GeminiEmbedder embeds q15 documents and search queries with Gemini.
type GeminiEmbedder struct {
	client     *genai.Client
	model      string
	dimensions int
}

// NewGeminiEmbedder constructs a Gemini-backed embedder.
func NewGeminiEmbedder(
	ctx context.Context,
	apiKey string,
	model string,
	dimensions int,
) (*GeminiEmbedder, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key is required")
	}
	model = normalizeModel(model)
	dimensions = normalizeDimensions(dimensions)
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be greater than 0")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create Gemini client: %w", err)
	}
	return &GeminiEmbedder{
		client:     client,
		model:      model,
		dimensions: dimensions,
	}, nil
}

// EmbedDocuments embeds retrieval documents in bounded Gemini batches.
func (g *GeminiEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("gemini embedder is not configured")
	}
	out := make([][]float32, 0, len(reqs))
	for start := 0; start < len(reqs); start += geminiEmbedBatchSize {
		end := min(start+geminiEmbedBatchSize, len(reqs))
		batch, err := g.embedBatch(ctx, reqs[start:end], "RETRIEVAL_DOCUMENT")
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// EmbedQuery embeds one retrieval query.
func (g *GeminiEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("query text is required")
	}
	vectors, err := g.embedContents(ctx, genai.Text(text), "RETRIEVAL_QUERY")
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("gemini returned %d query embeddings, want 1", len(vectors))
	}
	return vectors[0], nil
}

func (g *GeminiEmbedder) embedBatch(
	ctx context.Context,
	reqs []EmbeddingRequest,
	taskType string,
) ([][]float32, error) {
	contents := make([]*genai.Content, 0, len(reqs))
	for _, req := range reqs {
		text := strings.TrimSpace(req.Text)
		if text == "" {
			return nil, fmt.Errorf("document text is required")
		}
		contents = append(contents, genai.Text(text)...)
	}
	return g.embedContents(ctx, contents, taskType)
}

func (g *GeminiEmbedder) embedContents(
	ctx context.Context,
	contents []*genai.Content,
	taskType string,
) ([][]float32, error) {
	outputDimensions := int32(g.dimensions)
	var response *genai.EmbedContentResponse
	var err error
	for attempt := range geminiEmbedMaxAttempts {
		response, err = g.client.Models.EmbedContent(
			ctx,
			g.model,
			contents,
			&genai.EmbedContentConfig{
				TaskType:             taskType,
				OutputDimensionality: &outputDimensions,
			},
		)
		if err == nil {
			break
		}
		if !isRetriableGeminiEmbedError(err) || attempt == geminiEmbedMaxAttempts-1 {
			return nil, fmt.Errorf("gemini embed content: %w", err)
		}
		timer := time.NewTimer(geminiEmbedRetryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if len(response.Embeddings) != len(contents) {
		return nil, fmt.Errorf(
			"gemini returned %d embeddings for %d inputs",
			len(response.Embeddings),
			len(contents),
		)
	}
	out := make([][]float32, 0, len(response.Embeddings))
	for i, embedding := range response.Embeddings {
		if embedding == nil || len(embedding.Values) == 0 {
			return nil, fmt.Errorf("gemini returned empty embedding at index %d", i)
		}
		out = append(out, append([]float32(nil), embedding.Values...))
	}
	return out, nil
}

func isRetriableGeminiEmbedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "429") ||
		strings.Contains(message, "resource_exhausted") ||
		strings.Contains(message, "rate limit") ||
		strings.Contains(message, "quota") ||
		strings.Contains(message, "too many requests")
}

func geminiEmbedRetryDelay(attempt int) time.Duration {
	delays := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second,
	}
	if attempt < 0 {
		return delays[0]
	}
	if attempt >= len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt]
}
