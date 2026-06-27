package modelcatalog

import (
	"fmt"
	"strings"
	"sync"
)

// Selection stores the session-scoped current provider/model pair. It is the
// mutable layer on top of the live Registry: reads are cheap, while mutations
// are validated against the current roster.
type Selection struct {
	registry *Registry

	mu       sync.RWMutex
	provider string
	model    string
}

// NewSelection creates a session-scoped selection. The seed is intentionally
// not validated against the live roster: a configured or auto-selected model
// can disappear without preventing startup, and every runtime mutation via Set
// is validated against the current roster.
func NewSelection(registry *Registry, provider, model string) *Selection {
	return &Selection{
		registry: registry,
		provider: strings.TrimSpace(provider),
		model:    strings.TrimSpace(model),
	}
}

// CurrentProvider returns the selected provider name.
func (s *Selection) CurrentProvider() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider
}

// CurrentModel returns the selected agent-side model ref.
func (s *Selection) CurrentModel() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// Current returns the selected provider name and agent-side model ref.
func (s *Selection) Current() (string, string) {
	if s == nil {
		return "", ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider, s.model
}

// Set updates the current provider/model pair after validating that the exact
// provider-hosted model exists in the live roster.
func (s *Selection) Set(provider, model string) error {
	if s == nil {
		return fmt.Errorf("model selection is not configured")
	}
	if s.registry == nil {
		return fmt.Errorf("model registry is not configured")
	}

	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if model == "" {
		return fmt.Errorf("model is required")
	}
	if _, ok := s.registry.Lookup(provider, model); !ok {
		return fmt.Errorf(
			"model %q for provider %q is not in the current live roster",
			model,
			provider,
		)
	}

	s.mu.Lock()
	s.provider = provider
	s.model = model
	s.mu.Unlock()
	return nil
}
