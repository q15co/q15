package modelcatalog

import (
	"fmt"
	"strings"
)

// RoleSelection is the registry's runtime view of the held decision
// selection. Model is the agent-side ref; ProviderModel is the native model ID
// when known. Selections are runtime state: nothing in the config file binds
// them, and they are persisted by internal/selectionstore.
type RoleSelection struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ProviderModel string `json:"provider_model,omitempty"`
}

// IsValid reports whether the selection names a provider and a model.
func (s RoleSelection) IsValid() bool {
	return strings.TrimSpace(s.Provider) != "" && strings.TrimSpace(s.Model) != ""
}

// SetDecision replaces the held decision selection. An invalid selection
// clears the slot.
func (r *Registry) SetDecision(sel RoleSelection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decision = normalizeRoleSelection(sel)
}

// Decision returns the currently held decision selection, if any.
func (r *Registry) Decision() (RoleSelection, bool) {
	if r == nil {
		return RoleSelection{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.decision.IsValid() {
		return RoleSelection{}, false
	}
	return r.decision, true
}

func normalizeRoleSelection(sel RoleSelection) RoleSelection {
	return RoleSelection{
		Provider:      strings.TrimSpace(sel.Provider),
		Model:         strings.TrimSpace(sel.Model),
		ProviderModel: strings.TrimSpace(sel.ProviderModel),
	}
}

// RequireChat checks whether a model supports the chat operation.
func (r *Registry) RequireChat(role string, m Model) error {
	if !m.Capabilities.Chat {
		return fmt.Errorf("role %s model %q: missing capability chat", role, m.ProviderModel)
	}
	return nil
}
