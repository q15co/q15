package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OllamaEmbedder uses the native embedding API with truncation explicitly disabled.
type OllamaEmbedder struct {
	endpoint    string
	model       string
	dimensions  int
	batchSize   int
	batchTokens int
	client      *http.Client
}

type ollamaEmbeddingOptions struct {
	NumBatch int `json:"num_batch"`
}

// NewOllamaEmbedder binds a local model independently of library-search settings.
func NewOllamaEmbedder(
	baseURL, model string,
	dimensions, batchSize, batchTokens int,
) (*OllamaEmbedder, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.RawQuery != "" ||
		base.Fragment != "" ||
		base.User != nil {
		return nil, errors.New(
			"ollama embedding endpoint requires an HTTP URL without credentials or query",
		)
	}
	if strings.TrimSpace(model) == "" || dimensions < 1 || dimensions > 2000 || batchSize < 1 ||
		batchSize > 128 {
		return nil, errors.New("invalid Ollama embedding model, dimensions or batch size")
	}
	if batchTokens == 0 {
		batchTokens = 8192
	}
	if batchTokens < 1 || batchTokens > 65536 {
		return nil, errors.New("Ollama embedding token batch must be between 1 and 65536")
	}
	return &OllamaEmbedder{
		endpoint:    base.JoinPath("api/embed").String(),
		model:       model,
		dimensions:  dimensions,
		batchSize:   batchSize,
		batchTokens: batchTokens,
		client:      &http.Client{Timeout: 2 * time.Minute},
	}, nil
}

// EmbedQuery embeds one prepared retrieval query.
func (e *OllamaEmbedder) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vectors, err := e.EmbedDocuments(ctx, []EmbeddingRequest{{Text: query}})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// EmbedDocuments bounds batches and rejects truncated or malformed responses.
func (e *OllamaEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	result := make([][]float32, 0, len(reqs))
	for start := 0; start < len(reqs); start += e.batchSize {
		batch := reqs[start:min(start+e.batchSize, len(reqs))]
		input := make([]string, len(batch))
		for n, r := range batch {
			input[n] = r.Text
		}
		vectors, err := e.embed(ctx, input)
		if err != nil {
			return nil, err
		}
		result = append(result, vectors...)
	}
	return result, nil
}

func (e *OllamaEmbedder) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Model      string                 `json:"model"`
		Input      []string               `json:"input"`
		Dimensions int                    `json:"dimensions"`
		Truncate   bool                   `json:"truncate"`
		Options    ollamaEmbeddingOptions `json:"options"`
	}{
		Model:      e.model,
		Input:      input,
		Dimensions: e.dimensions,
		Truncate:   false,
		// Non-causal embedding models need the whole input to fit in a physical batch.
		Options: ollamaEmbeddingOptions{NumBatch: e.batchTokens},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errors.New("ollama embedding endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embedding endpoint returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return nil, errors.New("invalid Ollama embedding response")
	}
	if len(result.Embeddings) != len(input) {
		return nil, errors.New("ollama embedding response count differs from input")
	}
	for _, vector := range result.Embeddings {
		if len(vector) != e.dimensions {
			return nil, errors.New("ollama embedding response has unexpected dimensions")
		}
	}
	return result.Embeddings, nil
}
