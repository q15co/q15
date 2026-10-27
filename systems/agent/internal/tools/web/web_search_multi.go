package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const ollamaWebSearchURL = "https://ollama.com/api/web_search"

// Failure backoff: a provider that keeps failing is skipped for a cooldown so
// a dead backend cannot tax every other call with a full client timeout.
const (
	providerFailureThreshold = 3
	providerCooldown         = 60 * time.Second
)

// searchFunc performs one provider search and returns formatted results.
type searchFunc func(ctx context.Context, query string, count int) (string, error)

// provider is one search backend with its own throttle state.
type provider struct {
	name string
	run  searchFunc
	min  time.Duration // minimum spacing between calls

	mu   sync.Mutex
	last time.Time

	fails         int
	cooldownUntil time.Time
}

// wait blocks until this provider is allowed to run another request.
func (p *provider) wait(ctx context.Context) error {
	p.mu.Lock()
	delay := time.Duration(0)
	if !p.last.IsZero() {
		if remaining := p.min - time.Since(p.last); remaining > 0 {
			delay = remaining
		}
	}
	p.last = time.Now().Add(delay)
	p.mu.Unlock()

	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitRemaining reports how long until this provider may next run, without
// booking a slot.
func (p *provider) waitRemaining() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last.IsZero() {
		return 0
	}
	if remaining := p.min - time.Since(p.last); remaining > 0 {
		return remaining
	}
	return 0
}

// available reports whether the provider is outside its failure cooldown.
func (p *provider) available(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !now.Before(p.cooldownUntil)
}

// recordSuccess clears the failure streak.
func (p *provider) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails = 0
	p.cooldownUntil = time.Time{}
}

// recordFailure counts one failed request and starts a cooldown once the
// streak reaches the threshold.
func (p *provider) recordFailure() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails++
	if p.fails >= providerFailureThreshold {
		p.cooldownUntil = time.Now().Add(providerCooldown)
	}
}

// MultiWebSearch round-robins searches across configured providers, throttles
// each provider independently, and falls back to the next provider when one
// errors so a rate-limited or misconfigured backend does not fail the tool.
type MultiWebSearch struct {
	providers []provider
	maxRes    int

	mu   sync.Mutex
	next int
}

// NewMultiWebSearch builds the web_search tool from whichever provider keys are
// present. It returns nil when none are configured.
func NewMultiWebSearch(braveAPIKey, ollamaAPIKey string) (*MultiWebSearch, error) {
	m := &MultiWebSearch{maxRes: defaultWebSearchCount}

	if key := strings.TrimSpace(braveAPIKey); key != "" {
		brave, err := NewBraveWebSearch(key)
		if err != nil {
			return nil, err
		}
		m.providers = append(m.providers, provider{
			name: "brave",
			min:  1100 * time.Millisecond, // free tier allows ~1 request/second
			run: func(ctx context.Context, query string, count int) (string, error) {
				args, err := json.Marshal(map[string]any{"query": query, "count": count})
				if err != nil {
					return "", err
				}
				return brave.Run(ctx, string(args))
			},
		})
	}

	if key := strings.TrimSpace(ollamaAPIKey); key != "" {
		ollama, err := NewOllamaWebSearch(key)
		if err != nil {
			return nil, err
		}
		m.providers = append(m.providers, provider{
			name: "ollama",
			min:  250 * time.Millisecond,
			run:  ollama.Search,
		})
	}

	if len(m.providers) == 0 {
		return nil, nil
	}
	return m, nil
}

// Providers reports the configured provider names in rotation order.
func (m *MultiWebSearch) Providers() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.providers))
	for i := range m.providers {
		names = append(names, m.providers[i].name)
	}
	return names
}

// Definition returns the tool schema exposed to the model.
func (m *MultiWebSearch) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web for current information and return titles, URLs, and snippets. Requests rotate across the configured providers.",
		PromptGuidance: []string{
			"Use for discovery when the question depends on current web information.",
			"Keep queries concise: keyword phrases work best; full paragraphs are rejected or truncated by providers.",
			"After choosing a result, use web_fetch on the selected URL when you need the page contents.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Concise search query (max 400 characters and 50 words)",
					"maxLength":   maxBraveSearchQueryChars,
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

// Run executes one search, rotating the starting provider per call.
func (m *MultiWebSearch) Run(ctx context.Context, arguments string) (string, error) {
	if m == nil || len(m.providers) == 0 {
		return "", fmt.Errorf("web search tool is not configured")
	}

	var args struct {
		Query string `json:"query"`
		Count *int   `json:"count"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}

	query := normalizeSearchQuery(args.Query)
	if query == "" {
		return "", fmt.Errorf("missing required argument: query")
	}
	if err := validateSearchQuery(query); err != nil {
		return "", err
	}

	count := m.maxRes
	if count <= 0 {
		count = defaultWebSearchCount
	}
	if args.Count != nil {
		count = clamp(*args.Count, 1, maxWebSearchCount)
	}

	m.mu.Lock()
	start := m.next
	m.next = (m.next + 1) % len(m.providers)
	m.mu.Unlock()

	// Under contention the provider the rotation lands on may be throttled
	// while another is idle. Starting on the idle one keeps a rate-limited
	// backend from consuming the caller's deadline; rotation order is kept for
	// every other case, including the fallback walk below.
	order := m.order(start)

	failures := make([]error, 0, len(m.providers))
	for _, idx := range order {
		p := &m.providers[idx]
		if !p.available(time.Now()) {
			continue
		}
		if err := p.wait(ctx); err != nil {
			return "", err
		}
		out, err := p.run(ctx, query, count)
		if err == nil {
			p.recordSuccess()
			return out, nil
		}
		p.recordFailure()
		failures = append(failures, fmt.Errorf("%s: %w", p.name, err))
		if cerr := ctx.Err(); cerr != nil {
			failures = append(failures, cerr)
			break
		}
	}

	if len(failures) == 0 {
		return "", fmt.Errorf("every web search provider is cooling down after repeated failures")
	}
	return "", fmt.Errorf("all web search providers failed: %w", errors.Join(failures...))
}

// order returns provider indexes starting at start, moving the first provider
// that can run immediately to the front.
func (m *MultiWebSearch) order(start int) []int {
	order := make([]int, 0, len(m.providers))
	for i := 0; i < len(m.providers); i++ {
		order = append(order, (start+i)%len(m.providers))
	}
	now := time.Now()
	for i, idx := range order {
		if m.providers[idx].available(now) && m.providers[idx].waitRemaining() <= 0 {
			order[0], order[i] = order[i], order[0]
			break
		}
	}
	return order
}

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

// Search runs one query and formats the result list like the Brave backend.
func (o *OllamaWebSearch) Search(ctx context.Context, query string, count int) (string, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "max_results": count})
	if err != nil {
		return "", fmt.Errorf("encode ollama search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL, bytes.NewReader(payload))
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
