package modelcatalog

import (
	"context"
	"errors"
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
	catalog  Catalog
	interval time.Duration
	timeout  time.Duration

	refreshMu        sync.Mutex
	mu               sync.RWMutex
	providers        []Provider
	snap             []Model          // flat current roster
	byRef            map[string]Model // ref → Model (first provider wins on duplicate)
	statusByProvider map[string]Status
	// reload is the most recent attempt to apply an edited provider list, nil
	// until one runs.
	reload *ReloadStatus
}

// Status distinguishes discovery failures from successful empty rosters.
type Status struct {
	// ModelCount is the number of models contributed by the provider.
	ModelCount int
	// Error is the discovery failure, empty when the attempt succeeded.
	Error string
	// CheckedAt is when the attempt ran.
	CheckedAt time.Time
}

// Reason explains why the provider contributed no models. It is empty when the
// provider contributed at least one.
func (s Status) Reason() string {
	switch {
	case s.Error != "":
		return "discovery failed: " + s.Error
	case s.ModelCount == 0:
		return "discovery succeeded but the provider listed no models"
	default:
		return ""
	}
}

// ReloadStatus records whether the most recent provider configuration edit
// was applied, and explains a refusal.
type ReloadStatus struct {
	// CheckedAt is when the edit was applied or refused.
	CheckedAt time.Time
	// Applied is true when the attempt replaced the provider list.
	Applied bool
	// Error is why the attempt was refused, empty when it was applied.
	Error string
}

// New builds a live roster registry. providers is the initial provider list
// (with resolved API keys). interval and timeout default to 24h / 60s when zero;
// callers should pass explicit values (app.Start uses 10m / 10s).
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
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.catalog == nil {
		return
	}
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()
	if len(providers) == 0 {
		return
	}
	r.publish(providers, r.discover(ctx, providers))
}

type rosterSnapshot struct {
	models           []Model
	byRef            map[string]Model
	statusByProvider map[string]Status
}

func (r *Registry) discover(ctx context.Context, providers []Provider) rosterSnapshot {
	checkedAt := time.Now().UTC()
	statusByProvider := make(map[string]Status, len(providers))
	var combined []Model
	for _, p := range providers {
		pctx, cancel := context.WithTimeout(ctx, r.timeout)
		models, err := r.catalog.Discover(pctx, p)
		cancel()
		if err != nil {
			log.Printf("q15: discovery provider %q: %v; skipping this cycle", p.Name, err)
			statusByProvider[p.Name] = Status{Error: err.Error(), CheckedAt: checkedAt}
			continue
		}
		statusByProvider[p.Name] = Status{ModelCount: len(models), CheckedAt: checkedAt}
		models = append([]Model(nil), models...)
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

	return rosterSnapshot{models: combined, byRef: byRef, statusByProvider: statusByProvider}
}

func (r *Registry) publish(providers []Provider, roster rosterSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.snap = roster.models
	r.byRef = roster.byRef
	r.statusByProvider = roster.statusByProvider
}

// Run ticks at the configured interval, refreshing the roster until ctx is
// done. It does not perform an initial refresh — the caller is responsible for
// ensuring a populated roster before starting Run (app.Start does this
// synchronously). It never returns an error.
func (r *Registry) Run(ctx context.Context) error {
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

// Providers returns the current configured provider descriptors. Safe for
// concurrent use. Provider API keys are intentionally redacted from the copy so
// callers can safely use this for model-facing summaries.
func (r *Registry) Providers() []Provider {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.providers) == 0 {
		return nil
	}
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	for i := range out {
		out[i].APIKey = ""
	}
	return out
}

// ReplaceProviders discovers the candidate providers before publishing them
// together with their roster. A failed replacement leaves the running providers
// and models intact. Refresh and replacement are serialized so an older fetch
// cannot overwrite a newly applied configuration.
func (r *Registry) ReplaceProviders(ctx context.Context, providers []Provider) error {
	if len(providers) == 0 {
		return errors.New("refusing to apply an empty provider list")
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.catalog == nil {
		return errors.New("model catalog is not configured")
	}
	providers = append([]Provider(nil), providers...)
	roster := r.discover(ctx, providers)
	if len(roster.models) == 0 {
		return errors.New("the edited providers discovered no models")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.publish(providers, roster)
	return nil
}

// SetReloadStatus records the outcome of one attempt to apply an edited provider
// list. It is called by the composition root, which owns the config file. Safe
// for concurrent use.
func (r *Registry) SetReloadStatus(s ReloadStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reload = &s
}

// ReloadStatus returns the outcome of the most recent config-reload attempt, and
// whether one has run. Safe for concurrent use.
func (r *Registry) ReloadStatus() (ReloadStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.reload == nil {
		return ReloadStatus{}, false
	}
	return *r.reload, true
}

// Snapshot returns the current flat roster. Safe for concurrent use.
func (r *Registry) Snapshot() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, len(r.snap))
	copy(out, r.snap)
	return out
}

// LookupByRef finds one model by its agent-side ref. Version tags are preserved
// in normalized form (for example, "gpt-oss:20b" becomes "gpt-oss-20b"),
// while deployment markers are stripped. Returns ok=false when the model is not
// in the current roster (provider down or model deprecated).
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

// ProviderStatus returns the outcome of the most recent discovery attempt for
// providerName, and whether that provider has been queried yet. Safe for
// concurrent use.
func (r *Registry) ProviderStatus(providerName string) (Status, bool) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return Status{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.statusByProvider[providerName]
	return st, ok
}

// IsEmpty reports whether the current roster has zero models.
func (r *Registry) IsEmpty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.snap) == 0
}

// deriveRef produces the agent-side ref from a provider model ID. Version tags
// are preserved so tagged variants do not collide. Deployment suffixes
// ("-cloud", "-local") and bare deployment markers ("cloud", "local") are
// stripped, while "/" and remaining ":" separators are normalized to "-".
func deriveRef(providerModel string) string {
	s := strings.ToLower(strings.TrimSpace(providerModel))
	base, tag, tagged := strings.Cut(s, ":")
	if tagged && base != "" {
		tag = stripDeploymentSuffix(tag)
		if tag == "cloud" || tag == "local" {
			tag = ""
		}
		if tag == "" {
			s = base
		} else {
			s = base + ":" + tag
		}
	}
	s = strings.ReplaceAll(s, "/", "-")
	return strings.ReplaceAll(s, ":", "-")
}
