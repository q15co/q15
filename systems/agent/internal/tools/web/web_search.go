// Package web provides web search and web fetch tools for the agent.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const (
	defaultWebSearchCount    = 5
	maxWebSearchCount        = 10
	maxBraveSearchQueryChars = 400
	maxBraveSearchQueryWords = 50
	maxErrorResponseBody     = 200

	// providerTimeout is the one request budget every provider uses. Unequal
	// timeouts used to make fallback latency depend on which backend answered
	// first.
	providerTimeout = 20 * time.Second
	// providerResponseLimit bounds one provider response body. The ceiling
	// lives at the read as well as at the render so a hostile or careless
	// payload cannot be decoded into memory first.
	providerResponseLimit = 2 << 20

	// defaultSnippetChars is the snippet budget when the caller does not ask
	// for a different one.
	defaultSnippetChars = 300
	// maxSnippetChars is the largest snippet_chars a caller may request. It is
	// the excerpt bound, not a separate number: a rendered snippet longer than
	// a Result.Snippet would break the invariant this package is built around.
	maxSnippetChars = maxExcerptRunes
	// maxRenderBytes is the ceiling on one rendered response, measured on the
	// joined output. Results are dropped rather than letting the whole set
	// through.
	maxRenderBytes = 16 * 1024

	// Failure backoff: a provider that keeps failing is skipped for a cooldown
	// so a dead backend cannot tax every other call with a full client timeout.
	providerFailureThreshold = 3
	providerCooldown         = 60 * time.Second
)

// Result is one search hit, in the one shape every provider returns.
//
// Snippet carries a normalised excerpt, never a page body: it is cleaned with
// cleanText, deduplicated against the title, and at most maxExcerptRunes runes
// long. Published is the zero time when the provider has no date for the
// result.
type Result struct {
	Title     string
	URL       string
	Snippet   string    // invariant: normalised excerpt, <= maxExcerptRunes
	Published time.Time // zero when the provider has no date
}

// SearchRequest is the argument set passed to a provider. Later filters
// (freshness, offset, country, language) are additive here rather than a second
// signature change across every adapter.
type SearchRequest struct {
	Query string
	Count int
}

// Provider is one web search backend (Brave, Ollama, ...). Implementations
// are constructed from SearchConfig via NewProviders and composed into the
// web_search tool by NewSearch.
//
// Adapters return data only. No implementation renders, formats or joins text:
// the renderer in Search.Run is the single place the output shape is decided,
// so a new backend cannot invent a second shape. Search must return results
// whose Snippet is a normalised excerpt of at most maxExcerptRunes runes and
// whose URL is absolute; an adapter that returns whole documents fails the
// contract its callers rely on.
type Provider interface {
	// Name identifies the backend in the attribution line and error messages.
	Name() string
	// MinInterval is the minimum spacing between requests to this backend.
	MinInterval() time.Duration
	// Search runs one query and returns its results in the shared shape.
	Search(ctx context.Context, req SearchRequest) ([]Result, error)
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
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		delay := time.Duration(0)
		if !p.last.IsZero() {
			delay = p.min - time.Since(p.last)
		}
		if delay <= 0 {
			p.last = time.Now()
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
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

// Attribution reasons. Every selection path in Run maps to one of these, so
// the header names the path actually taken rather than a guess: a stale timer
// makes rotation order invisible from the outside, and mislabelling exactly the
// calls that are hardest to reproduce is worse than saying nothing.
const (
	reasonRotation  = "rotation"
	reasonIdle      = "idle"
	reasonThrottled = "throttled"
	reasonFailed    = "failed"
)

// selection is one planned provider attempt: the provider index, its name, and
// the reason it was chosen.
type selection struct {
	index  int
	name   string
	reason string
}

// fallbackReason names the provider that did not answer and why, so the
// attribution line reads "brave (fallback after ollama: throttled)".
func fallbackReason(name, cause string) string {
	return fmt.Sprintf("fallback after %s: %s", name, cause)
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

	snippetChars := defaultSnippetChars

	s.mu.Lock()
	start := s.next
	s.next = (s.next + 1) % len(s.providers)
	s.mu.Unlock()

	// Under contention the provider the rotation lands on may be throttled
	// while another is idle. Starting on the idle one keeps a rate-limited
	// backend from consuming the caller's deadline; rotation order is kept for
	// every other case, including the fallback walk below.
	plan := s.order(start)

	failures := make([]error, 0, len(s.providers))
	priorName, priorCause := "", ""
	for _, attempt := range plan {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		p := &s.providers[attempt.index]
		if !p.available(time.Now()) {
			priorName, priorCause = attempt.name, reasonThrottled
			continue
		}
		if err := p.wait(ctx); err != nil {
			return "", err
		}
		results, err := p.provider.Search(ctx, SearchRequest{Query: query, Count: count})
		if err == nil {
			p.recordSuccess()
			reason := attempt.reason
			if priorCause != "" {
				reason = fallbackReason(priorName, priorCause)
			}
			return renderResults(
				p.provider.Name(),
				reason,
				count,
				results,
				snippetChars,
			), nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", p.provider.Name(), err))
		if cerr := ctx.Err(); cerr != nil {
			failures = append(failures, cerr)
			break
		}
		p.recordFailure()
		priorName, priorCause = attempt.name, reasonFailed
	}

	if len(failures) == 0 {
		return "", fmt.Errorf("every web search provider is cooling down after repeated failures")
	}
	return "", fmt.Errorf("all web search providers failed: %w", errors.Join(failures...))
}

// order returns the attempt plan starting at start, moving the first provider
// that can run immediately to the front, and labels every entry with the reason
// it was selected.
//
// The head is "rotation" when the rotation target can run and "idle" when a
// provider was promoted over it. Every later entry is a "fallback after ..."
// naming its predecessor, because reaching it means the predecessor did not
// answer.
func (s *Search) order(start int) []selection {
	plan := make([]selection, 0, len(s.providers))
	for i := 0; i < len(s.providers); i++ {
		idx := (start + i) % len(s.providers)
		plan = append(plan, selection{index: idx, name: s.providers[idx].provider.Name()})
	}

	now := time.Now()
	promoted := false
	for i, attempt := range plan {
		if s.providers[attempt.index].available(now) && s.providers[attempt.index].waitRemaining() <= 0 {
			plan[0], plan[i] = plan[i], plan[0]
			promoted = i != 0
			break
		}
	}

	if plan[0].index == start && !promoted {
		plan[0].reason = reasonRotation
	} else {
		plan[0].reason = reasonIdle
	}

	for i := 1; i < len(plan); i++ {
		prior := &s.providers[plan[i-1].index]
		cause := reasonFailed
		if !prior.available(now) || prior.waitRemaining() > 0 {
			cause = reasonThrottled
		}
		plan[i].reason = fallbackReason(plan[i-1].name, cause)
	}
	return plan
}

// renderResults builds the one output shape every provider is rendered with:
// an attribution header naming the backend and the reason it answered, then one
// numbered block per result with the URL and snippet indented under it.
func renderResults(provider, reason string, requested int, results []Result, snippetChars int) string {
	var b strings.Builder

	header := provider
	if reason != "" {
		header = fmt.Sprintf("%s (%s)", provider, reason)
	}
	fmt.Fprintf(&b, "%s · %d results\n", header, len(results))
	if len(results) < requested {
		fmt.Fprintf(&b, "requested %d\n", requested)
	}

	written := 0
	for i, result := range results {
		block := renderResult(i+1, result, snippetChars)
		if b.Len()+len(block) > maxRenderBytes {
			if written == 0 {
				// Nothing but the header fitted: keep a bounded slice of this
				// block rather than dropping the only result silently.
				b.WriteString(truncateBytes(block, maxRenderBytes-b.Len()-32))
				b.WriteString("\n")
				written++
			}
			fmt.Fprintf(&b, "output truncated at %d bytes\n", maxRenderBytes)
			break
		}
		b.WriteString(block)
		written++
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderResult renders one result: numbered title line, indented URL line,
// indented snippet line.
func renderResult(n int, result Result, snippetChars int) string {
	var b strings.Builder

	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "%d. %s\n", n, title)

	urlLine := strings.TrimSpace(result.URL)
	if urlLine == "" {
		urlLine = "(no url)"
	}
	if isDocumentURL(urlLine) {
		urlLine += " [document]"
	}
	fmt.Fprintf(&b, "   %s\n", urlLine)

	if snippet := truncateSnippet(result.Snippet, snippetChars); snippet != "" {
		fmt.Fprintf(&b, "   %s\n", snippet)
	}
	if !result.Published.IsZero() {
		fmt.Fprintf(&b, "   published %s\n", result.Published.UTC().Format("2006-01-02"))
	}
	return b.String()
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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

func normalizeSearchQuery(query string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
}

func validateSearchQuery(query string) error {
	if charCount := utf8.RuneCountInString(query); charCount > maxBraveSearchQueryChars {
		return fmt.Errorf(
			"query is too long for Brave Search: %d characters, maximum is %d; retry with a shorter search query",
			charCount,
			maxBraveSearchQueryChars,
		)
	}
	if wordCount := len(strings.Fields(query)); wordCount > maxBraveSearchQueryWords {
		return fmt.Errorf(
			"query is too long for Brave Search: %d words, maximum is %d; retry with a concise keyword search",
			wordCount,
			maxBraveSearchQueryWords,
		)
	}
	return nil
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
