package web

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
	braveSearchURL = "https://api.search.brave.com/res/v1/web/search"
	// braveSearchMinInterval spaces requests: the free tier allows ~1 request/second.
	braveSearchMinInterval = 1100 * time.Millisecond
)

// BraveWebSearch queries the Brave Search API and returns structured results.
// It renders nothing: the shared renderer in web_search.go owns the output
// shape, and this adapter only maps the provider payload into Result values.
type BraveWebSearch struct {
	apiKey  string
	client  *http.Client
	baseURL string
}

// NewBraveWebSearch constructs a Brave-backed web search provider.
func NewBraveWebSearch(apiKey string) (*BraveWebSearch, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("brave api key is required")
	}

	return &BraveWebSearch{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: providerTimeout},
		baseURL: braveSearchURL,
	}, nil
}

// Definition returns the tool schema exposed to the model. It is the composed
// tool's definition, not a second copy: the adapter has no schema of its own.
func (b *BraveWebSearch) Definition() agent.ToolDefinition {
	tool, err := NewSearch([]Provider{b})
	if err != nil {
		return agent.ToolDefinition{Name: "web_search"}
	}
	return tool.Definition()
}

// Run executes one search with this adapter as the only backend. It exists so
// the adapter can be driven standalone; it delegates to the composed tool so
// argument parsing and rendering are not duplicated in the adapter.
func (b *BraveWebSearch) Run(ctx context.Context, arguments string) (string, error) {
	tool, err := NewSearch([]Provider{b})
	if err != nil {
		return "", err
	}
	return tool.Run(ctx, arguments)
}

// Name implements Provider.
func (b *BraveWebSearch) Name() string {
	return "brave"
}

// MinInterval implements Provider.
func (b *BraveWebSearch) MinInterval() time.Duration {
	return braveSearchMinInterval
}

// Search runs one Brave query and returns its results in the shared shape.
// Brave's description field is already a summary, so the excerpt path is close
// to a no-op on it; it is still applied so that markup, entities and whitespace
// are normalised the same way every other adapter's text is.
func (b *BraveWebSearch) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	if b == nil {
		return nil, fmt.Errorf("brave web search is not configured")
	}
	if b.client == nil {
		return nil, fmt.Errorf("brave web search http client is not configured")
	}

	count := req.Count
	if count <= 0 {
		count = defaultWebSearchCount
	}

	searchURL, err := url.Parse(b.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid brave search base url: %w", err)
	}
	q := searchURL.Query()
	q.Set("q", req.Query)
	q.Set("count", fmt.Sprintf("%d", count))
	// Term bolding is meant for a human reading a rendered page; in a snippet
	// handed to a model it is markup to be stripped again on our side.
	q.Set("text_decorations", "false")
	searchURL.RawQuery = q.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create brave search request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := b.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("brave search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, "brave search response")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
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
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse brave search response: %w", err)
	}

	terms := queryTerms(req.Query)
	results := make([]Result, 0, len(parsed.Web.Results))
	for _, item := range parsed.Web.Results {
		title := cleanText(item.Title)
		body := stripTitleEcho(title, cleanText(item.Description))
		results = append(results, Result{
			Title:     title,
			URL:       strings.TrimSpace(item.URL),
			Snippet:   excerpt(body, terms, maxExcerptRunes),
			Published: parsePublishedTime(item.PageAge),
		})
	}
	return results, nil
}

// parsePublishedTime converts the date shapes providers use into a time.Time,
// returning the zero time when the value is absent or unrecognised: a missing
// date must not fail the whole search.
func parsePublishedTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC1123,
		time.RFC1123Z,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// readLimited reads a provider response bounded by providerResponseLimit, so an
// oversized payload is cut at the read rather than after decoding it into
// memory. It is shared by both adapters.
func readLimited(body io.Reader, what string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, providerResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	if len(data) > providerResponseLimit {
		return nil, fmt.Errorf(
			"%s exceeded the %d byte read limit",
			what,
			providerResponseLimit,
		)
	}
	return data, nil
}
