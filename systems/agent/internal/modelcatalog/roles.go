package modelcatalog

import (
	"fmt"
	"strings"
)

// RoleSelection is the registry's runtime view of one held decision or embed
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

// matches reports whether the selection refers to the roster model.
func (s RoleSelection) matches(m Model) bool {
	return s.Provider == m.ProviderName &&
		(s.Model == m.Ref || s.Model == m.ProviderModel ||
			(s.ProviderModel != "" && s.ProviderModel == m.ProviderModel))
}

// SetRoleSelections replaces the held decision and embed selections at boot.
// An invalid (empty) selection clears its slot.
func (r *Registry) SetRoleSelections(decision, embed RoleSelection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decision = normalizeRoleSelection(decision)
	r.embed = normalizeRoleSelection(embed)
}

// SetDecision replaces the held decision selection. An invalid selection
// clears the slot.
func (r *Registry) SetDecision(sel RoleSelection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decision = normalizeRoleSelection(sel)
}

// SetEmbed replaces the held embed selection. An invalid selection clears the
// slot.
func (r *Registry) SetEmbed(sel RoleSelection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.embed = normalizeRoleSelection(sel)
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

// Embed returns the currently held embed selection, if any.
func (r *Registry) Embed() (RoleSelection, bool) {
	if r == nil {
		return RoleSelection{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.embed.IsValid() {
		return RoleSelection{}, false
	}
	return r.embed, true
}

func normalizeRoleSelection(sel RoleSelection) RoleSelection {
	return RoleSelection{
		Provider:      strings.TrimSpace(sel.Provider),
		Model:         strings.TrimSpace(sel.Model),
		ProviderModel: strings.TrimSpace(sel.ProviderModel),
	}
}

// RequireChat checks the effective chat capability and enforces the dual-role
// rule: the model currently selected as the decider never serves chat.
func (r *Registry) RequireChat(role string, m Model) error {
	if !m.Capabilities.Chat {
		return fmt.Errorf("role %s model %q: missing capability chat", role, m.ProviderModel)
	}
	if decision, ok := r.Decision(); ok && decision.matches(m) {
		return fmt.Errorf(
			"role %s model %q: selected as the decision model; pick another model or move the decision slot first (switch_model with slot decision)",
			role,
			m.ProviderModel,
		)
	}
	return nil
}
