package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

// Failure backoff: a provider that keeps failing is skipped for a cooldown so
// a dead backend cannot tax every other call with a full client timeout.
const (
	providerFailureThreshold = 3
	providerCooldown         = 60 * time.Second
)

// Provider is one web search backend (Brave, Ollama, ...). Implementations
// are constructed from SearchConfig via NewProviders and composed into the
// web_search tool by NewSearch.
type Provider interface {
	// Name identifies the backend in rotation reports and error messages.
	Name() string
	// MinInterval is the minimum spacing between requests to this backend.
	MinInterval() time.Duration
	// Search runs one query and returns the formatted result list.
	Search(ctx context.Context, query string, count int) (string, error)
}

// SearchConfig is the resolved web_search configuration: one field per
// supported provider, empty when that provider is not configured.
type SearchConfig struct {
	BraveAPIKey  string
	OllamaAPIKey string
}

// NewProviders builds one Provider per configured key, in rotation order. An
// unconfigured web_search resolves to an empty slice, in which case the tool
// is not registered at all.
func NewProviders(cfg SearchConfig) ([]Provider, error) {
	var providers []Provider

	if key := strings.TrimSpace(cfg.BraveAPIKey); key != "" {
		brave, err := NewBraveWebSearch(key)
		if err != nil {
			return nil, err
		}
		providers = append(providers, brave)
	}

	if key := strings.TrimSpace(cfg.OllamaAPIKey); key != "" {
		ollama, err := NewOllamaWebSearch(key)
		if err != nil {
			return nil, err
		}
		providers = append(providers, ollama)
	}

	return providers, nil
}

// throttledProvider couples a Provider with its request spacing and failure
// cooldown bookkeeping.
type throttledProvider struct {
	provider Provider
	min      time.Duration // minimum spacing between calls

	mu   sync.Mutex
	last time.Time

	fails         int
	cooldownUntil time.Time
}

// wait blocks until this provider is allowed to run another request.
func (p *throttledProvider) wait(ctx context.Context) error {
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
func (p *throttledProvider) waitRemaining() time.Duration {
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
func (p *throttledProvider) available(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !now.Before(p.cooldownUntil)
}

// recordSuccess clears the failure streak.
func (p *throttledProvider) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails = 0
	p.cooldownUntil = time.Time{}
}

// recordFailure counts one failed request and starts a cooldown once the
// streak reaches the threshold.
func (p *throttledProvider) recordFailure() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails++
	if p.fails >= providerFailureThreshold {
		p.cooldownUntil = time.Now().Add(providerCooldown)
	}
}

// Search is the web_search tool: it round-robins requests across the
// configured providers, throttles each provider independently, and falls back
// to the next provider when one errors so a rate-limited or misconfigured
// backend does not fail the tool.
type Search struct {
	providers []throttledProvider
	maxRes    int

	mu   sync.Mutex
	next int
}

// NewSearch composes the web_search tool from the resolved providers. It
// requires at least one provider: with none configured the tool is not
// registered, so constructing one would be a programming error.
func NewSearch(providers []Provider) (*Search, error) {
	if len(providers) == 0 {
		return nil, errors.New("web search requires at least one provider")
	}
	s := &Search{
		providers: make([]throttledProvider, len(providers)),
		maxRes:    defaultWebSearchCount,
	}
	for i, p := range providers {
		if p == nil {
			return nil, fmt.Errorf("web search provider %d is nil", i)
		}
		s.providers[i] = throttledProvider{provider: p, min: p.MinInterval()}
	}
	return s, nil
}

// Providers reports the configured provider names in rotation order.
func (s *Search) Providers() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.providers))
	for i := range s.providers {
		names = append(names, s.providers[i].provider.Name())
	}
	return names
}

// Definition returns the tool schema exposed to the model.
func (s *Search) Definition() agent.ToolDefinition {
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
func (s *Search) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || len(s.providers) == 0 {
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

	count := s.maxRes
	if count <= 0 {
		count = defaultWebSearchCount
	}
	if args.Count != nil {
		count = clamp(*args.Count, 1, maxWebSearchCount)
	}

	s.mu.Lock()
	start := s.next
	s.next = (s.next + 1) % len(s.providers)
	s.mu.Unlock()

	// Under contention the provider the rotation lands on may be throttled
	// while another is idle. Starting on the idle one keeps a rate-limited
	// backend from consuming the caller's deadline; rotation order is kept for
	// every other case, including the fallback walk below.
	order := s.order(start)

	failures := make([]error, 0, len(s.providers))
	for _, idx := range order {
		p := &s.providers[idx]
		if !p.available(time.Now()) {
			continue
		}
		if err := p.wait(ctx); err != nil {
			return "", err
		}
		out, err := p.provider.Search(ctx, query, count)
		if err == nil {
			p.recordSuccess()
			return out, nil
		}
		p.recordFailure()
		failures = append(failures, fmt.Errorf("%s: %w", p.provider.Name(), err))
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
func (s *Search) order(start int) []int {
	order := make([]int, 0, len(s.providers))
	for i := 0; i < len(s.providers); i++ {
		order = append(order, (start+i)%len(s.providers))
	}
	now := time.Now()
	for i, idx := range order {
		if s.providers[idx].available(now) && s.providers[idx].waitRemaining() <= 0 {
			order[0], order[i] = order[i], order[0]
			break
		}
	}
	return order
}
