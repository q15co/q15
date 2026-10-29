package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ollamaWebSearchURL = "https://ollama.com/api/web_search"
	// ollamaSearchMinInterval spaces requests to the hosted Ollama search API.
	ollamaSearchMinInterval = 250 * time.Millisecond
)

// OllamaWebSearch queries Ollama's hosted web search API.
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
		client:  &http.Client{Timeout: 20 * time.Second},
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

// Search runs one query and formats the result list like the Brave backend.
func (o *OllamaWebSearch) Search(ctx context.Context, query string, count int) (string, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "max_results": count})
	if err != nil {
		return "", fmt.Errorf("encode ollama search request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		o.baseURL,
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", fmt.Errorf("create ollama search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read ollama search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
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
		return "", fmt.Errorf("parse ollama search response: %w", err)
	}
	if len(parsed.Results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	lines := []string{fmt.Sprintf("Results for: %s", query)}
	for i, item := range parsed.Results {
		if i >= count {
			break
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, title))
		lines = append(lines, fmt.Sprintf("   %s", strings.TrimSpace(item.URL)))
		if snippet := strings.TrimSpace(item.Content); snippet != "" {
			lines = append(lines, fmt.Sprintf("   %s", snippet))
		}
	}
	return strings.Join(lines, "\n"), nil
}
