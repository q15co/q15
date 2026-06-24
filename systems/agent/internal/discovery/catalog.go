package discovery

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	ollamaprovider "github.com/q15co/q15/systems/agent/internal/provider/ollama"
	openaicodexprovider "github.com/q15co/q15/systems/agent/internal/provider/openaicodex"
	openaicompatibleprovider "github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

const (
	// defaultHTTPTimeout caps individual discovery HTTP calls when the caller
	// does not supply a configured *http.Client.
	defaultHTTPTimeout = 30 * time.Second
)

// defaultCatalogModelsDevURL is the models.dev source URL used by NewCatalog.
// It is a variable (not a constant) so tests can point it at an httptest
// server without changing the package's public API.
var defaultCatalogModelsDevURL = modelsDevAPIURL

type rosterDiscoverer interface {
	Discover(context.Context, modelcatalog.Provider) ([]modelcatalog.Model, error)
}

// defaultCatalog composes provider-owned roster adapters with models.dev
// enrichment.
type defaultCatalog struct {
	rosters   map[string]rosterDiscoverer
	modelsdev *modelsDevClient
}

// NewCatalog constructs the default Catalog from provider roster adapters and
// the models.dev enrichment client. When httpClient is nil a client with a 30s
// timeout is used.
func NewCatalog(httpClient *http.Client) Catalog {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &defaultCatalog{
		rosters: map[string]rosterDiscoverer{
			providertypes.Ollama:           ollamaprovider.NewRosterClient(httpClient),
			providertypes.OpenAICompatible: openaicompatibleprovider.NewRosterClient(httpClient),
			providertypes.OpenAICodex:      openaicodexprovider.NewRosterClient(),
		},
		modelsdev: newModelsDevClientWithSource(httpClient, defaultCatalogModelsDevURL),
	}
}

// Discover queries the provider's roster endpoint, optionally enriches from
// models.dev, then applies include/exclude glob filters.
func (c *defaultCatalog) Discover(ctx context.Context, p Provider) ([]Model, error) {
	providerType := providertypes.MustNormalize(p.Type)
	roster, ok := c.rosters[providerType]
	if !ok {
		return nil, fmt.Errorf("discovery: unsupported provider type %q", p.Type)
	}

	base, err := roster.Discover(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(base) == 0 {
		return nil, nil
	}
	if p.Options.ModelsDev {
		if enriched, enrichErr := c.modelsdev.Enrich(ctx, p, base); enrichErr == nil &&
			len(enriched) > 0 {
			base = modelcatalog.Merge(base, enriched)
		}
		// models.dev enrichment failures are non-fatal: proceed with the base
		// roster unchanged.
	}
	return modelcatalog.ApplyFilters(base, p.Options.Include, p.Options.Exclude), nil
}
