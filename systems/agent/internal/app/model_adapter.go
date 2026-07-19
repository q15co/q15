package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/provider/ollama"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicodex"
	"github.com/q15co/q15/systems/agent/internal/provider/openaicompatible"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
)

// routedModelAdapter resolves an agent-side model ref to a live provider client
// each turn. It is both an agent.ModelClient and a modelselection.Planner. The
// optional Selection disambiguates a ref that more than one provider hosts by
// preferring the currently selected provider/model pair.
type routedModelAdapter struct {
	registry   *modelcatalog.Registry
	selection  *modelcatalog.Selection
	mediaStore q15media.Store
	factory    modelClientFactory
	clients    sync.Map // providerName → agent.ModelClient
}

// modelClientFactory builds a provider client for one discovered model.
type modelClientFactory func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error)

// providerModelBinder resolves one exact provider and agent-side model ref to
// a client pinned to the current roster entry. Binding once per scheduled run
// prevents a roster refresh between model turns from changing its target.
type providerModelBinder interface {
	BindProviderModel(string, string) (agent.ModelClient, error)
}

var errProviderModelUnavailable = errors.New("provider model is not in the current roster")

var (
	_ agent.ModelClient   = (*routedModelAdapter)(nil)
	_ providerModelBinder = (*routedModelAdapter)(nil)
	_ agent.ModelClient   = boundProviderModelClient{}
)

// Complete resolves the model ref to a live model, reuses or builds the
// provider client, suppresses tools for non-tool models, and adapts media to
// the model's capabilities before delegating to the provider client.
func (r *routedModelAdapter) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	model = strings.TrimSpace(model)
	m, ok := r.lookupModel(model)
	if !ok {
		return agent.ModelClientResult{}, fmt.Errorf(
			"model %q is not in the current roster (provider down or model deprecated)",
			model,
		)
	}
	return r.completeModel(ctx, m, messages, tools)
}

// BindProviderModel resolves an exact provider and agent-side model ref once.
// The returned client deliberately bypasses selection and ref-only lookup, so
// it cannot silently fall back or change target during a multi-turn run.
func (r *routedModelAdapter) BindProviderModel(
	providerName string,
	modelRef string,
) (agent.ModelClient, error) {
	providerName = strings.TrimSpace(providerName)
	modelRef = strings.TrimSpace(modelRef)
	var (
		m  modelcatalog.Model
		ok bool
	)
	if r != nil && r.registry != nil {
		m, ok = r.registry.Lookup(providerName, modelRef)
	}
	if !ok {
		return nil, fmt.Errorf(
			"%w: model %q for provider %q (provider down or model deprecated)",
			errProviderModelUnavailable,
			modelRef,
			providerName,
		)
	}
	return boundProviderModelClient{
		adapter: r,
		model:   m,
		ref:     modelRef,
	}, nil
}

type boundProviderModelClient struct {
	adapter *routedModelAdapter
	model   modelcatalog.Model
	ref     string
}

func (c boundProviderModelClient) Complete(
	ctx context.Context,
	modelRef string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef != c.ref {
		return agent.ModelClientResult{}, fmt.Errorf(
			"bound model ref %q does not match requested ref %q",
			c.ref,
			modelRef,
		)
	}
	return c.adapter.completeModel(ctx, c.model, messages, tools)
}

// completeModel applies the provider client, capability, and media adaptation
// path shared by ref-only and provider-qualified completion.
func (r *routedModelAdapter) completeModel(
	ctx context.Context,
	m modelcatalog.Model,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	client, err := r.getOrCreateClient(m)
	if err != nil {
		return agent.ModelClientResult{}, err
	}

	if !m.Capabilities.ToolCalling {
		tools = nil
	}

	adapted := q15media.AdaptMediaToCapabilities(
		messages,
		q15media.SupportFromCapabilities(m.Capabilities),
		r.mediaStore,
	)
	return client.Complete(ctx, m.ProviderModel, adapted, tools)
}

// lookupModel resolves a ref to a live model, preferring the currently selected
// provider/model pair when the ref matches the current model.
func (r *routedModelAdapter) lookupModel(ref string) (modelcatalog.Model, bool) {
	ref = strings.TrimSpace(ref)
	if r == nil || r.registry == nil || ref == "" {
		return modelcatalog.Model{}, false
	}
	if r.selection != nil {
		provider, currentRef := r.selection.Current()
		if provider != "" && ref == currentRef {
			if m, ok := r.registry.Lookup(provider, ref); ok {
				return m, true
			}
		}
	}
	return r.registry.LookupByRef(ref)
}

// getOrCreateClient returns a cached provider client or builds one.
func (r *routedModelAdapter) getOrCreateClient(
	m modelcatalog.Model,
) (agent.ModelClient, error) {
	if cached, ok := r.clients.Load(m.ProviderName); ok {
		return cached.(agent.ModelClient), nil
	}
	client, err := r.factory(m, r.mediaStore)
	if err != nil {
		return nil, fmt.Errorf("configure provider %q: %w", m.ProviderName, err)
	}
	r.clients.Store(m.ProviderName, client)
	return client, nil
}

// newModelAdapterWithFactory builds a routed adapter without a selection (so
// duplicate refs resolve to the first provider). It is primarily a test helper.
func newModelAdapterWithFactory(
	registry *modelcatalog.Registry,
	mediaStore q15media.Store,
	clientFactory modelClientFactory,
) (*routedModelAdapter, error) {
	return newModelAdapterWithSelectionAndFactory(registry, nil, mediaStore, clientFactory)
}

// newModelAdapterWithSelectionAndFactory is the underlying constructor.
func newModelAdapterWithSelectionAndFactory(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	mediaStore q15media.Store,
	clientFactory modelClientFactory,
) (*routedModelAdapter, error) {
	if registry == nil {
		return nil, errors.New("model registry is required")
	}
	if clientFactory == nil {
		return nil, errors.New("model client factory is required")
	}
	return &routedModelAdapter{
		registry:   registry,
		selection:  selection,
		mediaStore: mediaStore,
		factory:    clientFactory,
	}, nil
}

// defaultModelClientFactory builds the provider client for a discovered model
// based on its canonical provider type.
func defaultModelClientFactory(
	m modelcatalog.Model,
	mediaStore q15media.Store,
) (agent.ModelClient, error) {
	return makeDumpAwareFactory(nil)(m, mediaStore)
}

// makeDumpAwareFactory returns a modelClientFactory that threads an optional
// dump transport through to each provider's NewClient. When rt is nil the
// factory uses default HTTP transports.
func makeDumpAwareFactory(rt http.RoundTripper) modelClientFactory {
	return func(m modelcatalog.Model, mediaStore q15media.Store) (agent.ModelClient, error) {
		switch providertypes.MustNormalize(m.ProviderType) {
		case providertypes.Ollama:
			return ollama.NewClient(m.ProviderBaseURL, m.ProviderAPIKey, mediaStore, rt)
		case providertypes.OpenAICompatible:
			return openaicompatible.NewClient(m.ProviderBaseURL, m.ProviderAPIKey, mediaStore, rt)
		case providertypes.OpenAICodex:
			return openaicodex.NewClient(mediaStore, rt)
		default:
			return nil, fmt.Errorf("unsupported provider type %q", m.ProviderType)
		}
	}
}

// openDumpWriter reads Q15_DUMP_PAYLOADS and returns a writer for JSONL payload
// capture. An empty or unset env var returns (nil, nil). The special value
// "stderr" writes to os.Stderr. Any other value is treated as a file path
// (opened append-only). The closer is non-nil only for file-backed writers.
func openDumpWriter() (io.Writer, func()) {
	path := strings.TrimSpace(os.Getenv("Q15_DUMP_PAYLOADS"))
	if path == "" {
		return nil, nil
	}
	if path == "stderr" {
		return os.Stderr, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("q15: Q15_DUMP_PAYLOADS open %q failed: %v (dump disabled)", path, err)
		return nil, nil
	}
	return f, func() {
		if err := f.Close(); err != nil {
			log.Printf("q15: Q15_DUMP_PAYLOADS close %q failed: %v", path, err)
		}
	}
}

// buildModelRefs returns the engine's eligible model-ref list: the current
// model first, then the remaining roster snapshot refs (deduped). This is the
// honest, live fallback — if the current model is gone, the engine falls
// through to other roster models.
func buildModelRefs(currentRef string, registry *modelcatalog.Registry) []string {
	currentRef = strings.TrimSpace(currentRef)
	snapshot := registry.Snapshot()

	refs := make([]string, 0, len(snapshot)+1)
	seen := make(map[string]struct{}, len(snapshot)+1)

	if currentRef != "" {
		refs = append(refs, currentRef)
		seen[currentRef] = struct{}{}
	}
	for _, m := range snapshot {
		if _, ok := seen[m.Ref]; ok {
			continue
		}
		refs = append(refs, m.Ref)
		seen[m.Ref] = struct{}{}
	}
	return refs
}

// loadInteractiveSelection materializes the in-memory interactive model
// selection from the persisted selection store, auto-selecting and persisting
// a first-eligible roster model on first run.
func loadInteractiveSelection(
	registry *modelcatalog.Registry,
	store *selectionstore.Store,
) (*modelcatalog.Selection, error) {
	if registry == nil || store == nil {
		return nil, errors.New("registry and selection store are required")
	}

	if current := store.Interactive(); current.IsValid() {
		return modelcatalog.NewSelection(registry, current.Provider, current.Model), nil
	}

	provider, model, ok := autoSelectModel(registry)
	if !ok {
		return nil, errors.New(
			"no models available in the live roster — cannot bootstrap model selection",
		)
	}
	if err := store.SetInteractive(provider, model); err != nil {
		return nil, fmt.Errorf("persist auto-selected model: %w", err)
	}
	return modelcatalog.NewSelection(registry, provider, model), nil
}

// autoSelectModel picks the first-eligible roster model for bootstrap. It
// prefers a text+tool-calling model (so the agent keeps its tools), then falls
// back to any text-capable model. Returns false when the roster is empty.
func autoSelectModel(registry *modelcatalog.Registry) (string, string, bool) {
	if registry == nil {
		return "", "", false
	}
	snapshot := registry.Snapshot()
	var textFallback *modelcatalog.Model
	for i := range snapshot {
		m := &snapshot[i]
		if !m.Capabilities.Text {
			continue
		}
		if m.Capabilities.ToolCalling {
			return m.ProviderName, m.Ref, true
		}
		if textFallback == nil {
			textFallback = m
		}
	}
	if textFallback != nil {
		return textFallback.ProviderName, textFallback.Ref, true
	}
	return "", "", false
}

// newCognitionRefResolver returns a per-job model ref resolver for the
// cognition runner: a job with a persisted override runs on that model when it
// is still in the live roster, otherwise it inherits the interactive model.
func newCognitionRefResolver(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	store *selectionstore.Store,
) cognition.ModelRefResolver {
	return func(jobType string) []string {
		if override, ok := store.Cognition(jobType); ok {
			if _, exists := registry.Lookup(override.Provider, override.Model); exists {
				return buildModelRefs(override.Model, registry)
			}
		}
		return buildModelRefs(selection.CurrentModel(), registry)
	}
}

// cognitionJobTypeNames returns the stable job type identifiers for a set of
// cognition job registrations, used to validate switch_cognition_model.
func cognitionJobTypeNames(registrations []cognition.JobRegistration) []string {
	out := make([]string, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if registration.NewJob == nil {
			continue
		}
		jobType := strings.TrimSpace(registration.NewJob().Type())
		if jobType == "" {
			continue
		}
		if _, ok := seen[jobType]; ok {
			continue
		}
		seen[jobType] = struct{}{}
		out = append(out, jobType)
	}
	return out
}
