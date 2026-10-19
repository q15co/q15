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

// Check dispatches the currently selected decision model and rechecks its
// effective capability at the boundary, after resolution and before any client
// construction.
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
	if !model.Capabilities.Has("decision") {
		return decision.Response{}, fmt.Errorf(
			"role decision model %q: missing capability decision",
			model.ProviderModel,
		)
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

// resolve reads the decision slot fresh on every call: the selection is
// runtime state, so a switch_model slot decision takes effect on the next
// decision without a restart.
func (r *routedDecisionAdapter) resolve(ref string) (modelcatalog.Model, error) {
	if r == nil || r.registry == nil || r.factory == nil {
		return modelcatalog.Model{}, fmt.Errorf(
			"decision provider registry and client factory are required",
		)
	}
	selection, ok := r.registry.Decision()
	if !ok {
		return modelcatalog.Model{}, fmt.Errorf(
			"role decision: no model selected; use switch_model with slot decision",
		)
	}
	ref = strings.TrimSpace(ref)
	for _, model := range r.registry.Snapshot() {
		if model.ProviderName == selection.Provider &&
			(selection.Model == model.Ref || selection.Model == model.ProviderModel) {
			if ref != "" && ref != model.Ref && ref != model.ProviderModel {
				return modelcatalog.Model{}, fmt.Errorf(
					"role decision model %q: requested model differs from the selected decision model %q",
					ref,
					selection.Model,
				)
			}
			return model, nil
		}
	}
	return modelcatalog.Model{}, fmt.Errorf(
		"role decision model %q: missing capability decision in the live catalog",
		selection.Model,
	)
}

func (r *routedDecisionAdapter) clientFor(model modelcatalog.Model) (decision.Client, error) {
	key := providerClientKey{
		name:         model.ProviderName,
		providerType: model.ProviderType,
		baseURL:      model.ProviderBaseURL,
		apiKey:       model.ProviderAPIKey,
		decisionPath: model.ProviderDecisionPath,
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
		return ollama.NewClient(
			model.ProviderBaseURL,
			model.ProviderAPIKey,
			nil,
			nil,
			model.ProviderDecisionPath,
		)
	case providertypes.OpenAICompatible:
		return openaicompatible.NewDecisionClient(
			model.ProviderBaseURL,
			model.ProviderAPIKey,
			model.ProviderDecisionPath,
			nil,
		)
	case providertypes.SystemOneCompatible:
		return systemone.NewClient(
			model.ProviderBaseURL,
			model.ProviderAPIKey,
			model.ProviderDecisionPath,
			nil,
		)
	default:
		return nil, fmt.Errorf("unsupported decision provider type %q", model.ProviderType)
	}
}

// ContractVersion identifies the implemented decision port.
func (r *routedDecisionAdapter) ContractVersion() string { return decision.ContractVersion }

// decisionClientFor reuses the provider instance already owned by the chat router.
func (r *routedModelAdapter) decisionClientFor(model modelcatalog.Model) (decision.Client, error) {
	if providertypes.NormalizeOrEmpty(model.ProviderType) != providertypes.Ollama {
		return defaultDecisionClientFactory(model)
	}
	client, err := r.clientFor(model)
	if err != nil {
		return nil, err
	}
	port, ok := client.(decision.Client)
	if !ok {
		return nil, fmt.Errorf(
			"role decision provider %q: client lacks the decision port",
			model.ProviderName,
		)
	}
	return port, nil
}
