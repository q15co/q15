package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	openaiEmbedBatchSize   = 128
	openaiEmbedMaxAttempts = 6
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAIEmbedder embeds q15 documents and search queries against any
// OpenAI-compatible /embeddings endpoint (OpenAI, Jina, Ollama /v1, TEI).
type OpenAIEmbedder struct {
	client      *http.Client
	baseURL     string
	apiKey      string
	model       string
	dimensions  int
	batchSize   int
	retryDelays []time.Duration
}

// NewOpenAIEmbedder constructs an OpenAI-compatible embedder. apiKey may be
// empty only when baseURL is set (local unauthenticated endpoint); baseURL
// itself defaults to OpenAI when empty. model and dimensions are required and
// never fall back to the Gemini defaults. batchSize <= 0 uses the provider
// default; negative batch size is an error.
func NewOpenAIEmbedder(
	baseURL string,
	apiKey string,
	model string,
	dimensions int,
	batchSize int,
) (*OpenAIEmbedder, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" && baseURL == "" {
		return nil, fmt.Errorf("openai API key is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("openai embedding model is required")
	}
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be greater than 0")
	}
	if batchSize < 0 {
		return nil, fmt.Errorf("embedding batch size must not be negative")
	}
	if batchSize == 0 {
		batchSize = openaiEmbedBatchSize
	}
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return &OpenAIEmbedder{
		client:      &http.Client{},
		baseURL:     baseURL,
		apiKey:      apiKey,
		model:       model,
		dimensions:  dimensions,
		batchSize:   batchSize,
		retryDelays: defaultOpenAIEmbedRetryDelays(),
	}, nil
}

type openAIEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedding `json:"data"`
}

type openAIEmbedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type openAIStatusError struct {
	statusCode int
	body       string
}

func (e *openAIStatusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.statusCode, e.body)
}

// EmbedDocuments embeds retrieval documents in bounded OpenAI-compatible
// batches.
func (o *OpenAIEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	if o == nil || o.client == nil {
		return nil, fmt.Errorf("openai embedder is not configured")
	}
	out := make([][]float32, 0, len(reqs))
	for start := 0; start < len(reqs); start += o.batchSize {
		end := min(start+o.batchSize, len(reqs))
		batch, err := o.embedBatch(ctx, reqs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// EmbedQuery embeds one retrieval query.
func (o *OpenAIEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("query text is required")
	}
	vectors, err := o.embedInputs(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("openai returned %d query embeddings, want 1", len(vectors))
	}
	return vectors[0], nil
}

func (o *OpenAIEmbedder) embedBatch(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	inputs := make([]string, 0, len(reqs))
	for _, req := range reqs {
		text := strings.TrimSpace(req.Text)
		if text == "" {
			return nil, fmt.Errorf("document text is required")
		}
		inputs = append(inputs, text)
	}
	return o.embedInputs(ctx, inputs)
}

func (o *OpenAIEmbedder) embedInputs(
	ctx context.Context,
	inputs []string,
) ([][]float32, error) {
	payload, err := json.Marshal(openAIEmbedRequest{
		Model:          o.model,
		Input:          inputs,
		EncodingFormat: "float",
		Dimensions:     o.dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai embeddings request: %w", err)
	}
	var response []openAIEmbedding
	for attempt := range openaiEmbedMaxAttempts {
		response, err = o.postEmbeddings(ctx, payload)
		if err == nil {
			break
		}
		if !isRetriableOpenAIEmbedError(err) || attempt == openaiEmbedMaxAttempts-1 {
			return nil, fmt.Errorf("openai embeddings request: %w", err)
		}
		timer := time.NewTimer(o.retryDelay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if len(response) != len(inputs) {
		return nil, fmt.Errorf(
			"openai returned %d embeddings for %d inputs",
			len(response),
			len(inputs),
		)
	}
	sort.SliceStable(response, func(i, j int) bool {
		return response[i].Index < response[j].Index
	})
	out := make([][]float32, 0, len(response))
	for i, embedding := range response {
		if embedding.Index != i {
			return nil, fmt.Errorf(
				"openai returned embedding index %d at position %d",
				embedding.Index,
				i,
			)
		}
		if len(embedding.Embedding) == 0 {
			return nil, fmt.Errorf("openai returned empty embedding at index %d", i)
		}
		if o.dimensions > 0 && len(embedding.Embedding) != o.dimensions {
			return nil, fmt.Errorf(
				"openai returned embedding with %d dimensions at index %d, want %d",
				len(embedding.Embedding),
				i,
				o.dimensions,
			)
		}
		out = append(out, append([]float32(nil), embedding.Embedding...))
	}
	return out, nil
}

func (o *OpenAIEmbedder) postEmbeddings(
	ctx context.Context,
	payload []byte,
) ([]openAIEmbedding, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		o.baseURL+"/embeddings",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build openai embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &openAIStatusError{
			statusCode: resp.StatusCode,
			body:       readOpenAIErrorBody(resp.Body),
		}
	}
	var decoded openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode openai embeddings response: %w", err)
	}
	return decoded.Data, nil
}

func readOpenAIErrorBody(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func isRetriableOpenAIEmbedError(err error) bool {
	var statusErr *openAIStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.statusCode {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// defaultOpenAIEmbedRetryDelays returns a fresh retry backoff ladder so tests
// can stub per-instance delays without mutating shared state.
func defaultOpenAIEmbedRetryDelays() []time.Duration {
	return []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second,
	}
}

func (o *OpenAIEmbedder) retryDelay(attempt int) time.Duration {
	delays := o.retryDelays
	if len(delays) == 0 {
		return 0
	}
	if attempt < 0 {
		return delays[0]
	}
	if attempt >= len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt]
}
