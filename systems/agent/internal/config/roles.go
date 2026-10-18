package config

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// ModelBinding assigns an explicit provider/model pair to a deployment role.
type ModelBinding struct {
	Provider      string     `yaml:"provider"`
	Model         string     `yaml:"model"`
	AllowDualBind string     `yaml:"allow_dual_bind,omitempty"`
	Limits        RoleLimits `yaml:"limits,omitempty"`
}

// RoleLimits partitions request rate and canonical request-byte budgets per minute.
type RoleLimits struct {
	RequestsPerMinute     int `yaml:"requests_per_minute"`
	RequestBytesPerMinute int `yaml:"request_bytes_per_minute"`
}

var roleCapabilities = map[string]string{
	"primary":   "chat",
	"fallbacks": "chat",
	"decision":  "decision",
	"embed":     "embed",
}

func (c Config) roleBindings() []modelcatalog.RoleBinding {
	var out []modelcatalog.RoleBinding
	add := func(role string, b *ModelBinding) {
		if b != nil {
			out = append(
				out,
				modelcatalog.RoleBinding{
					Role:                  role,
					Provider:              strings.TrimSpace(b.Provider),
					Model:                 strings.TrimSpace(b.Model),
					AllowDualBind:         strings.TrimSpace(b.AllowDualBind),
					RequestsPerMinute:     b.Limits.RequestsPerMinute,
					RequestBytesPerMinute: b.Limits.RequestBytesPerMinute,
				},
			)
		}
	}
	add("primary", c.Primary)
	for i := range c.Fallbacks {
		add(fmt.Sprintf("fallbacks[%d]", i), &c.Fallbacks[i])
	}
	add("decision", c.Decision)
	add("embed", c.Embed)
	return out
}

func (c Config) validateRoleShapes() error {
	for _, b := range c.roleBindings() {
		if b.Provider == "" || b.Model == "" {
			return fmt.Errorf(
				"role %s model %q: explicit provider and model are required",
				b.Role,
				b.Model,
			)
		}
		if _, ok := c.FindProvider(b.Provider); !ok {
			return fmt.Errorf("role %s model %q: unknown provider %q", b.Role, b.Model, b.Provider)
		}
		if b.RequestsPerMinute < 0 || b.RequestBytesPerMinute < 0 {
			return fmt.Errorf("role %s model %q: limits must not be negative", b.Role, b.Model)
		}
		if b.AllowDualBind != "" && b.Role != "decision" {
			return fmt.Errorf(
				"role %s model %q: allow_dual_bind belongs on the decision binding",
				b.Role,
				b.Model,
			)
		}
	}
	return nil
}

// ValidateRoles resolves every explicit binding after discovery and before starting any worker.
// Dispatch rechecks the effective capabilities because discovery can change later.
func (c Config) ValidateRoles(registry *modelcatalog.Registry) ([]modelcatalog.RoleBinding, error) {
	return c.ValidateRoleModels(registry.Snapshot())
}

// ValidateRoleModels checks prospective roster data before an atomic provider reload.
func (c Config) ValidateRoleModels(
	models []modelcatalog.Model,
) ([]modelcatalog.RoleBinding, error) {
	if c.Agent != nil && c.Primary == nil {
		return nil, fmt.Errorf(
			"role primary model %q: missing capability chat; explicit binding is required",
			"<unbound>",
		)
	}
	if c.Agent != nil && c.Agent.Tools.Embeddings.configured() && c.Embed == nil {
		return nil, fmt.Errorf(
			"role embed model %q: missing capability embed; explicit binding is required",
			c.Agent.Tools.Embeddings.Model,
		)
	}
	bindings := c.roleBindings()
	resolved := make(map[string]modelcatalog.Model, len(bindings))
	for _, b := range bindings {
		role := b.Role
		if strings.HasPrefix(role, "fallbacks[") {
			role = "fallbacks"
		}
		required := roleCapabilities[role]
		var model modelcatalog.Model
		found := false
		for _, candidate := range models {
			if candidate.ProviderName == b.Provider &&
				(candidate.ProviderModel == b.Model || candidate.Ref == b.Model) {
				model = candidate
				found = true
				break
			}
		}
		if !found || !model.Capabilities.Has(required) {
			return nil, fmt.Errorf(
				"role %s model %q: missing capability %s in the live catalog",
				b.Role,
				b.Model,
				required,
			)
		}
		resolved[b.Role] = model
	}
	decisionModel, hasDecision := resolved["decision"]
	if hasDecision {
		for _, b := range bindings {
			if b.Role != "primary" && !strings.HasPrefix(b.Role, "fallbacks[") {
				continue
			}
			chat := resolved[b.Role]
			if chat.ProviderName != decisionModel.ProviderName ||
				chat.ProviderModel != decisionModel.ProviderModel {
				continue
			}
			if c.Decision == nil || strings.TrimSpace(c.Decision.AllowDualBind) == "" {
				return nil, fmt.Errorf(
					"role %s model %q: dual bind with decision requires allow_dual_bind",
					b.Role,
					b.Model,
				)
			}
			if b.RequestsPerMinute <= 0 || b.RequestBytesPerMinute <= 0 ||
				c.Decision.Limits.RequestsPerMinute <= 0 ||
				c.Decision.Limits.RequestBytesPerMinute <= 0 {
				return nil, fmt.Errorf(
					"role %s model %q: dual bind requires separate role rate and budget limits",
					b.Role,
					b.Model,
				)
			}
		}
	}
	return bindings, nil
}
