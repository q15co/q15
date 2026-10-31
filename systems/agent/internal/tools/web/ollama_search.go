package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ollamaWebSearchURL = "https://ollama.com/api/web_search"
	// ollamaSearchMinInterval spaces requests to the hosted Ollama search API.
	ollamaSearchMinInterval = 250 * time.Millisecond
)

// OllamaWebSearch queries Ollama's hosted web search API and returns structured
// results. It renders nothing: the shared renderer owns the output shape.
type OllamaWebSearch struct {
	apiKey  string
	client  *http.Client
	baseURL string
}

// NewOllamaWebSearch constructs an Ollama-backed search provider.
func NewOllamaWebSearch(apiKey string) (*OllamaWebSearch, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("ollama api key is required")
	}
	return &OllamaWebSearch{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: providerTimeout},
		baseURL: ollamaWebSearchURL,
	}, nil
}

// Name implements Provider.
func (o *OllamaWebSearch) Name() string {
	return "ollama"
}

// MinInterval implements Provider.
func (o *OllamaWebSearch) MinInterval() time.Duration {
	return ollamaSearchMinInterval
}

// Search runs one query and returns its results in the shared shape. Ollama's
// content field is a page body rather than a summary, and it carries no date,
// so Published stays zero.
func (o *OllamaWebSearch) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	if o == nil {
		return nil, fmt.Errorf("ollama web search is not configured")
	}
	if o.client == nil {
		return nil, fmt.Errorf("ollama web search http client is not configured")
	}

	count := req.Count
	if count <= 0 {
		count = defaultWebSearchCount
	}

	payload, err := json.Marshal(map[string]any{"query": req.Query, "max_results": count})
	if err != nil {
		return nil, fmt.Errorf("encode ollama search request: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		o.baseURL,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create ollama search request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ollama search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, "ollama search response")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"ollama search API returned %s: %s",
			resp.Status,
			trimForError(string(body), maxErrorResponseBody),
		)
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse ollama search response: %w", err)
	}

	terms := queryTerms(req.Query)
	results := make([]Result, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		title := cleanText(item.Title)
		results = append(results, Result{
			Title:   title,
			URL:     strings.TrimSpace(item.URL),
			Snippet: documentExcerpt(title, item.Content, terms),
		})
	}
	return results, nil
}
