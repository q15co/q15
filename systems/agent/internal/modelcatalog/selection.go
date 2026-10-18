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
// not validated here: the composition root validates startup bindings before
// constructing a selection. Every runtime mutation via Set is checked against
// the current roster and the role policy.
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
	resolved, ok := s.registry.Lookup(provider, model)
	if !ok {
		return fmt.Errorf(
			"model %q for provider %q is not in the current live roster",
			model,
			provider,
		)
	}
	if err := s.registry.RequireChat("primary", resolved); err != nil {
		return err
	}

	s.mu.Lock()
	s.provider = provider
	s.model = model
	s.mu.Unlock()
	return nil
}
