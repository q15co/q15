package modelcatalog

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	// defaultRefreshInterval is how often the roster is re-fetched.
	defaultRefreshInterval = 24 * time.Hour
	// defaultProviderTimeout caps each individual provider discovery call.
	defaultProviderTimeout = 60 * time.Second
)

// Registry holds the live model roster, refreshed on a ticker. It is the
// single source of truth for which models exist and their metadata. Discovery
// is mandatory and live: every provider is queried, failures are logged and
// skipped (no last-known-good cache), and a roster change is visible to the
// next turn without restart.
type Registry struct {
	providers []Provider
	catalog   Catalog
	interval  time.Duration
	timeout   time.Duration

	mu    sync.RWMutex
	snap  []Model          // flat current roster
	byRef map[string]Model // ref → Model (first provider wins on duplicate)
}

// New builds a live roster registry. providers is the immutable provider list
// (with resolved API keys). interval and timeout default to 10m / 10s when zero.
func New(providers []Provider, catalog Catalog, interval, timeout time.Duration) *Registry {
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	if timeout <= 0 {
		timeout = defaultProviderTimeout
	}
	return &Registry{
		providers: providers,
		catalog:   catalog,
		interval:  interval,
		timeout:   timeout,
	}
}

// Refresh queries every provider now (best-effort) and replaces the snapshot.
// It never returns an error — provider failures are logged and skipped. The
// previous snapshot is replaced atomically.
func (r *Registry) Refresh(ctx context.Context) {
	if r.catalog == nil || len(r.providers) == 0 {
		return
	}

	var combined []Model
	for _, p := range r.providers {
		pctx, cancel := context.WithTimeout(ctx, r.timeout)
		models, err := r.catalog.Discover(pctx, p)
		cancel()
		if err != nil {
			log.Printf("q15: discovery provider %q: %v; skipping this cycle", p.Name, err)
			continue
		}
		for i := range models {
			// Annotate with provider metadata so consumers (adapter, factory)
			// have everything they need without a separate lookup.
			models[i].ProviderName = p.Name
			models[i].ProviderType = p.Type
			models[i].ProviderBaseURL = p.BaseURL
			models[i].ProviderAPIKey = p.APIKey
			models[i].Ref = deriveRef(models[i].ProviderModel)
		}
		combined = append(combined, models...)
	}

	byRef := make(map[string]Model, len(combined))
	for _, m := range combined {
		if _, exists := byRef[m.Ref]; !exists {
			byRef[m.Ref] = m
		}
	}

	r.mu.Lock()
	r.snap = combined
	r.byRef = byRef
	r.mu.Unlock()
}

// Run does one refresh, then ticks at the configured interval until ctx is
// done. It never returns an error.
func (r *Registry) Run(ctx context.Context) error {
	r.Refresh(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.Refresh(ctx)
		}
	}
}

// Snapshot returns the current flat roster. Safe for concurrent use.
func (r *Registry) Snapshot() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, len(r.snap))
	copy(out, r.snap)
	return out
}

// LookupByRef finds one model by its agent-side ref (tag-stripped provider
// model id). Returns ok=false when the model is not in the current roster
// (provider down or model deprecated).
func (r *Registry) LookupByRef(ref string) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byRef[ref]
	return m, ok
}

// IsEmpty reports whether the current roster has zero models.
func (r *Registry) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.snap) == 0
}

// deriveRef produces the agent-side ref from a provider model ID: Ollama
// ":tag" suffixes are stripped and "/" separators are replaced with "-".
func deriveRef(providerModel string) string {
	s := strings.TrimSpace(providerModel)
	if idx := strings.Index(s, ":"); idx > 0 {
		s = s[:idx]
	}
	return strings.ReplaceAll(s, "/", "-")
}
