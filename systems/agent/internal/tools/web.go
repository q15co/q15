// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const (
	braveSearchURL        = "https://api.search.brave.com/res/v1/web/search"
	defaultWebSearchCount = 5
	maxWebSearchCount     = 10
	maxErrorResponseBody  = 200
)

// BraveWebSearch queries the Brave Search API and formats a text result list.
type BraveWebSearch struct {
	apiKey     string
	maxResults int
	client     *http.Client
	baseURL    string
}

// NewBraveWebSearch constructs a Brave-backed web search tool.
func NewBraveWebSearch(apiKey string) (*BraveWebSearch, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("brave api key is required")
	}

	return &BraveWebSearch{
		apiKey:     apiKey,
		maxResults: defaultWebSearchCount,
		client:     &http.Client{Timeout: 10 * time.Second},
		baseURL:    braveSearchURL,
	}, nil
}

// Definition returns the tool schema exposed to the model.
func (b *BraveWebSearch) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web for current information using Brave Search and return titles, URLs, and snippets.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
				"count": map[string]any{
					"type":        "integer",
					"description": "Number of results to return (1-10)",
					"minimum":     1,
					"maximum":     maxWebSearchCount,
				},
			},
			"required": []string{"query"},
		},
	}
}

// Run executes one Brave search request from raw JSON tool arguments.
func (b *BraveWebSearch) Run(ctx context.Context, arguments string) (string, error) {
	if b == nil {
		return "", fmt.Errorf("web search tool is not configured")
	}
	if b.client == nil {
		return "", fmt.Errorf("web search http client is not configured")
	}

	var args struct {
		Query string `json:"query"`
		Count *int   `json:"count"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}

	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("missing required argument: query")
	}

	count := b.maxResults
	if count <= 0 {
		count = defaultWebSearchCount
	}
	if args.Count != nil {
		count = clamp(*args.Count, 1, maxWebSearchCount)
	}

	searchURL, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid brave search base url: %w", err)
	}
	q := searchURL.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	searchURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create brave search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("brave search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read brave search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"brave search API returned %s: %s",
			resp.Status,
			trimForError(string(body), maxErrorResponseBody),
		)
	}

	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse brave search response: %w", err)
	}

	if len(parsed.Web.Results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	lines := []string{fmt.Sprintf("Results for: %s", query)}
	for i, item := range parsed.Web.Results {
		if i >= count {
			break
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(untitled)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, title))
		lines = append(lines, fmt.Sprintf("   %s", strings.TrimSpace(item.URL)))
		if desc := strings.TrimSpace(item.Description); desc != "" {
			lines = append(lines, fmt.Sprintf("   %s", desc))
		}
	}

	return strings.Join(lines, "\n"), nil
}

func trimForError(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty response body)"
	}
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func clamp(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}
