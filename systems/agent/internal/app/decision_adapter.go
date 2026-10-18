package app

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/provider/ollama"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	"github.com/q15co/q15/systems/agent/internal/provider/systemone"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

type decisionClientFactory func(modelcatalog.Model) (decision.Client, error)

type routedDecisionAdapter struct {
	registry *modelcatalog.Registry
	factory  decisionClientFactory
	clients  sync.Map
}

var _ decision.Client = (*routedDecisionAdapter)(nil)

func newRoutedDecisionAdapter(
	registry *modelcatalog.Registry,
	factory decisionClientFactory,
) *routedDecisionAdapter {
	return &routedDecisionAdapter{registry: registry, factory: factory}
}

// Check resolves an optional agent-side model ref or auto-selects the first discovered decision model.
func (r *routedDecisionAdapter) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	if err := ctx.Err(); err != nil {
		return decision.Response{}, err
	}
	model, err := r.resolve(request.Model)
	if err != nil {
		return decision.Response{}, err
	}
	request.Model = model.ProviderModel
	if err := request.Validate(); err != nil {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: %w", err)
	}
	client, err := r.clientFor(model)
	if err != nil {
		return decision.Response{}, err
	}
	return client.Check(ctx, request)
}

func (r *routedDecisionAdapter) resolve(ref string) (modelcatalog.Model, error) {
	if r == nil || r.registry == nil || r.factory == nil {
		return modelcatalog.Model{}, fmt.Errorf(
			"decision provider registry and client factory are required",
		)
	}
	ref = strings.TrimSpace(ref)
	for _, model := range r.registry.Snapshot() {
		if model.IsDecision() && (ref == "" || ref == model.Ref) {
			return model, nil
		}
	}
	return modelcatalog.Model{}, fmt.Errorf(
		"no matching decision model in the live roster; check list_models with capability decision",
	)
}

func (r *routedDecisionAdapter) clientFor(model modelcatalog.Model) (decision.Client, error) {
	key := providerClientKey{
		name:         model.ProviderName,
		providerType: model.ProviderType,
		baseURL:      model.ProviderBaseURL,
		apiKey:       model.ProviderAPIKey,
	}
	if cached, ok := r.clients.Load(key); ok {
		return cached.(decision.Client), nil
	}
	client, err := r.factory(model)
	if err != nil {
		return nil, fmt.Errorf("configure decision provider %q: %w", model.ProviderName, err)
	}
	cached, _ := r.clients.LoadOrStore(key, client)
	return cached.(decision.Client), nil
}

// In-tree Go adapters are checked by the compiler. A runtime contract assertion is
// reserved for out-of-tree adapters; q15 has no runtime adapter loader.
func defaultDecisionClientFactory(model modelcatalog.Model) (decision.Client, error) {
	switch providertypes.NormalizeOrEmpty(model.ProviderType) {
	case providertypes.Ollama:
		return ollama.NewDecisionClient(model.ProviderBaseURL, model.ProviderAPIKey, nil)
	case providertypes.OpenAICompatible:
		return openaicompatible.NewDecisionClient(model.ProviderBaseURL, model.ProviderAPIKey, nil)
	case providertypes.SystemOneCompatible:
		return systemone.NewClient(model.ProviderBaseURL, model.ProviderAPIKey, nil)
	default:
		return nil, fmt.Errorf("unsupported decision provider type %q", model.ProviderType)
	}
}

// ContractVersion identifies the implemented decision port.
func (r *routedDecisionAdapter) ContractVersion() string { return decision.ContractVersion }
