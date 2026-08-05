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
// (with resolved API keys). interval and timeout default to 24h / 60s when zero.
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
		// Backward compat: register the legacy tag-stripped ref as an alias
		// when it doesn't collide with an existing ref. This lets persisted
		// configs using the old stripped form (e.g. "gpt-oss" for
		// "gpt-oss:20b") continue to resolve after the version-tag-preserving
		// change to deriveRef.
		if legacy := ModelKey(m.ProviderModel); legacy != m.Ref {
			if _, exists := byRef[legacy]; !exists {
				byRef[legacy] = m
			}
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

// Providers returns the immutable configured provider descriptors. Safe for
// concurrent use. Provider API keys are intentionally redacted from the copy so
// callers can safely use this for model-facing summaries.
func (r *Registry) Providers() []Provider {
	if r == nil || len(r.providers) == 0 {
		return nil
	}
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	for i := range out {
		out[i].APIKey = ""
	}
	return out
}

// Snapshot returns the current flat roster. Safe for concurrent use.
func (r *Registry) Snapshot() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, len(r.snap))
	copy(out, r.snap)
	return out
}

// LookupByRef finds one model by its agent-side ref. Returns ok=false when
// the model is not in the current roster (provider down or model deprecated).
// The ref is the agent-side identifier produced by deriveRef: version tags
// are preserved (e.g. "deepseek-v4-flash-0731"), deployment suffixes are
// stripped (e.g. "kimi-k2.7-code").
func (r *Registry) LookupByRef(ref string) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.byRef[strings.TrimSpace(ref)]
	return m, ok
}

// Lookup finds one model by provider name and agent-side ref. It disambiguates
// duplicate refs that are hosted by more than one provider.
func (r *Registry) Lookup(providerName, ref string) (Model, bool) {
	providerName = strings.TrimSpace(providerName)
	ref = strings.TrimSpace(ref)
	if providerName == "" || ref == "" {
		return Model{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.snap {
		if m.ProviderName == providerName && m.Ref == ref {
			return m, true
		}
	}
	return Model{}, false
}

// ProviderHasModels reports whether providerName contributed at least one model
// to the current live roster.
func (r *Registry) ProviderHasModels(providerName string) bool {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.snap {
		if m.ProviderName == providerName {
			return true
		}
	}
	return false
}

// IsEmpty reports whether the current roster has zero models.
func (r *Registry) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.snap) == 0
}

// deploymentSuffixes are Ollama deployment markers stripped from tags during
// ref derivation and candidate-key generation. They identify the hosting
// environment (cloud, local) and are not part of the model identity.
var deploymentSuffixes = []string{"-cloud", "-local"}

// bareDeploymentMarkers are tag values that consist solely of a deployment
// marker (no version/size component). They are dropped entirely.
var bareDeploymentMarkers = map[string]bool{"cloud": true, "local": true}

// stripDeploymentSuffix removes known deployment suffixes from a tag. It is
// shared between deriveRef and CandidateKeys so both stay in lockstep.
func stripDeploymentSuffix(tag string) string {
	for _, suffix := range deploymentSuffixes {
		tag = strings.TrimSuffix(tag, suffix)
	}
	return tag
}

// deriveRef produces the agent-side ref from a provider model ID. Version
// tags are preserved (e.g. "deepseek-v4-flash:0731" → "deepseek-v4-flash-0731")
// so colliding models remain distinguishable. Deployment suffixes like
// "-cloud" are stripped from the tag, and bare deployment markers ("cloud",
// "local") are dropped entirely. "/" and remaining ":" separators are replaced
// with "-".
func deriveRef(providerModel string) string {
	s := strings.ToLower(strings.TrimSpace(providerModel))

	if idx := strings.Index(s, ":"); idx > 0 {
		base := s[:idx]
		tag := s[idx+1:]

		// Strip deployment suffixes (e.g. "0731-cloud" → "0731").
		tag = stripDeploymentSuffix(tag)
		// Strip bare deployment markers.
		if bareDeploymentMarkers[tag] {
			tag = ""
		}

		if tag == "" {
			s = base
		} else {
			s = base + "-" + tag
		}
	}

	// Replace all path/colon separators with "-".
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return s
}
