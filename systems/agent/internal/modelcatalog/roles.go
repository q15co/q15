package modelcatalog

import (
	"fmt"
	"strings"
	"time"
)

// RoleBinding is the validated runtime view of an explicit configuration binding.
type RoleBinding struct {
	Role                  string `json:"role"`
	Provider              string `json:"provider"`
	Model                 string `json:"model"`
	AllowDualBind         string `json:"allow_dual_bind,omitempty"`
	RequestsPerMinute     int    `json:"requests_per_minute,omitempty"`
	RequestBytesPerMinute int    `json:"request_bytes_per_minute,omitempty"`
}

type roleUsage struct {
	since           time.Time
	requests, bytes int
}

// SetRoleBindings publishes validated bindings and resets their independent quota windows.
func (r *Registry) SetRoleBindings(bindings []RoleBinding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roles = append([]RoleBinding(nil), bindings...)
	r.roleUsage = make(map[string]roleUsage)
}

// RoleBindings returns the explicit bindings surfaced alongside provider status.
func (r *Registry) RoleBindings() []RoleBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]RoleBinding(nil), r.roles...)
}

// Binding returns the configured target for a role, without searching by capability.
func (r *Registry) Binding(role string) (RoleBinding, bool) {
	for _, b := range r.RoleBindings() {
		if b.Role == role {
			return b, true
		}
	}
	return RoleBinding{}, false
}

// RequireChat checks effective capability and the decider binding for the supplied chat role.
func (r *Registry) RequireChat(role string, m Model) error {
	if !m.Capabilities.Chat {
		return fmt.Errorf("role %s model %q: missing capability chat", role, m.ProviderModel)
	}
	decision, ok := r.Binding("decision")
	if !ok || !r.matches(decision, m) {
		return nil
	}
	if strings.TrimSpace(decision.AllowDualBind) == "" {
		return fmt.Errorf(
			"role %s model %q: dual bind with decision requires allow_dual_bind",
			role,
			m.ProviderModel,
		)
	}
	for _, b := range r.RoleBindings() {
		if (b.Role == "primary" || strings.HasPrefix(b.Role, "fallbacks[")) && r.matches(b, m) &&
			b.RequestsPerMinute > 0 &&
			b.RequestBytesPerMinute > 0 &&
			decision.RequestsPerMinute > 0 &&
			decision.RequestBytesPerMinute > 0 {
			return nil
		}
	}
	return fmt.Errorf(
		"role %s model %q: dual bind requires separate role rate and budget limits",
		role,
		m.ProviderModel,
	)
}

func (r *Registry) matches(b RoleBinding, m Model) bool {
	return b.Provider == m.ProviderName && (b.Model == m.ProviderModel || b.Model == m.Ref)
}

// ReserveRole atomically charges a role's canonical request bytes and request rate before I/O.
func (r *Registry) ReserveRole(role string, m Model, bytes int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.roles {
		matchesRole := b.Role == role ||
			(role == "primary" && strings.HasPrefix(b.Role, "fallbacks["))
		if !matchesRole || !r.matches(b, m) {
			continue
		}
		now := time.Now()
		usage := r.roleUsage[b.Role]
		if now.Sub(usage.since) >= time.Minute {
			usage = roleUsage{since: now}
		}
		if b.RequestsPerMinute > 0 && usage.requests >= b.RequestsPerMinute {
			return fmt.Errorf("role %s model %q: request rate exhausted", b.Role, m.ProviderModel)
		}
		if b.RequestBytesPerMinute > 0 && bytes > b.RequestBytesPerMinute-usage.bytes {
			return fmt.Errorf(
				"role %s model %q: request byte budget exhausted",
				b.Role,
				m.ProviderModel,
			)
		}
		usage.requests++
		usage.bytes += bytes
		if r.roleUsage == nil {
			r.roleUsage = make(map[string]roleUsage)
		}
		r.roleUsage[b.Role] = usage
		return nil
	}
	return nil
}
