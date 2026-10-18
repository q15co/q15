package config

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type roleCatalog []modelcatalog.Model

func (c roleCatalog) Discover(
	context.Context,
	modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return c, nil
}
func roleRegistry(caps modelcatalog.Capabilities) *modelcatalog.Registry {
	r := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "api"}},
		roleCatalog{{ProviderModel: "model", Capabilities: caps}},
		time.Hour,
		time.Second,
	)
	r.Refresh(context.Background())
	return r
}
func TestRoleValidationRejectsMissingCapability(t *testing.T) {
	for _, role := range []string{"primary", "fallbacks[0]", "decision", "embed"} {
		t.Run(role, func(t *testing.T) {
			b := &ModelBinding{Provider: "api", Model: "model"}
			cfg := Config{}
			switch role {
			case "primary":
				cfg.Primary = b
			case "fallbacks[0]":
				cfg.Fallbacks = []ModelBinding{*b}
			case "decision":
				cfg.Decision = b
			case "embed":
				cfg.Embed = b
			}
			_, err := cfg.ValidateRoles(roleRegistry(modelcatalog.Capabilities{}))
			required := "chat"
			if role == "decision" {
				required = "decision"
			}
			if role == "embed" {
				required = "embed"
			}
			if err == nil || !strings.Contains(err.Error(), role) ||
				!strings.Contains(err.Error(), "model") ||
				!strings.Contains(err.Error(), required) {
				t.Fatalf("role validation = %v", err)
			}
		})
	}
}
func TestDualBindAbortsWithoutOptIn(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		b := ModelBinding{Provider: "api", Model: "model"}
		decider := b
		cfg := Config{Decision: &decider}
		if fallback {
			cfg.Fallbacks = []ModelBinding{b}
		} else {
			cfg.Primary = &b
		}
		reg := roleRegistry(modelcatalog.Capabilities{Chat: true, Decision: true})
		if _, err := cfg.ValidateRoles(reg); err == nil {
			t.Fatal("dual bind accepted without reason")
		}
		cfg.Decision.AllowDualBind = "single model"
		if _, err := cfg.ValidateRoles(reg); err == nil {
			t.Fatal("dual bind accepted without partitions")
		}
		cfg.Decision.Limits = RoleLimits{RequestsPerMinute: 1, RequestBytesPerMinute: 1000}
		if fallback {
			cfg.Fallbacks[0].Limits = cfg.Decision.Limits
		} else {
			cfg.Primary.Limits = cfg.Decision.Limits
		}
		if _, err := cfg.ValidateRoles(reg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredAgentRequiresPrimaryAndEmbedBindings(t *testing.T) {
	cfg := Config{Agent: &Agent{Name: "q15"}}
	registry := roleRegistry(modelcatalog.Capabilities{Chat: true, Embed: true})
	if _, err := cfg.ValidateRoles(registry); err == nil ||
		!strings.Contains(err.Error(), "role primary") {
		t.Fatalf("unbound primary = %v", err)
	}
	cfg.Primary = &ModelBinding{Provider: "api", Model: "model"}
	cfg.Agent.Tools.Embeddings.Model = "model"
	if _, err := cfg.ValidateRoles(registry); err == nil ||
		!strings.Contains(err.Error(), "role embed") {
		t.Fatalf("unbound embed = %v", err)
	}
	cfg.Embed = &ModelBinding{Provider: "api", Model: "model"}
	if _, err := cfg.ValidateRoles(registry); err != nil {
		t.Fatal(err)
	}
}
