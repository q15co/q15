// Package app wires the q15 agent runtime and startup flow.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/discovery"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

const (
	// ConfigPath is the fixed container path for the mounted agent config.
	ConfigPath = "/etc/q15/agent/config.yaml"
	// AuthPath is the fixed container path for the mounted auth store.
	AuthPath = "/etc/q15/auth/auth.json"

	// registryRefreshInterval is how often the roster is re-fetched.
	registryRefreshInterval = 10 * time.Minute
	// registryProviderTimeout caps each individual provider discovery call.
	registryProviderTimeout = 10 * time.Second
)

// Run starts the agent runtime.
func Run(ctx context.Context, args []string) error {
	if err := validateArgs(args); err != nil {
		return err
	}
	return Start(ctx)
}

// Start loads config, builds the live model roster registry, and starts the bot.
func Start(ctx context.Context) error {
	if err := q15auth.SetStorePath(AuthPath); err != nil {
		return fmt.Errorf("configure auth store: %w", err)
	}

	cfg, err := config.Load(ConfigPath)
	if err != nil {
		return err
	}
	if cfg.Agent == nil {
		return fmt.Errorf("agent config %q does not define an agent", ConfigPath)
	}

	runtime, err := cfg.ResolveAgentRuntime()
	if err != nil {
		return err
	}
	if runtime == nil {
		return fmt.Errorf("agent config %q does not define an agent", ConfigPath)
	}

	// Build the provider descriptors with resolved API keys.
	providers, err := buildProviderDescriptors(cfg.Providers)
	if err != nil {
		return err
	}

	// Build the live roster registry (composition root).
	catalog := discovery.NewCatalog(http.DefaultClient)
	registry := modelcatalog.New(
		providers,
		catalog,
		registryRefreshInterval,
		registryProviderTimeout,
	)

	// Do the first refresh synchronously so the first turn has a roster.
	registry.Refresh(ctx)
	if registry.IsEmpty() {
		return errors.New(
			"model discovery returned no models — check provider connectivity and API keys",
		)
	}

	// Background refresh: keep the roster live without restart.
	go func() {
		_ = registry.Run(ctx)
	}()

	return runBot(ctx, *runtime, registry)
}

// buildProviderDescriptors converts config providers into modelcatalog
// providers with resolved API keys.
func buildProviderDescriptors(configProviders []config.Provider) ([]modelcatalog.Provider, error) {
	out := make([]modelcatalog.Provider, 0, len(configProviders))
	for _, p := range configProviders {
		providerType := providertypes.MustNormalize(p.Type)
		if providerType == "" {
			return nil, fmt.Errorf("provider %q has unsupported type %q", p.Name, p.Type)
		}
		apiKey, err := config.ResolveProviderAPIKey(p, providerType)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", p.Name, err)
		}
		out = append(out, modelcatalog.Provider{
			Name:    strings.TrimSpace(p.Name),
			Type:    providerType,
			BaseURL: strings.TrimSpace(p.BaseURL),
			APIKey:  apiKey,
			Options: modelcatalog.Options{
				ModelsDev: p.Discovery.ModelsDev,
				Include:   p.Discovery.Include,
				Exclude:   p.Discovery.Exclude,
			},
		})
	}
	return out, nil
}

func validateArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return errors.New("q15-agent accepts no arguments")
}
