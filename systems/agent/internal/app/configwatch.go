package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// configWatcher reloads provider configuration on the roster interval. Only
// the config file is hashed; rotating a secret alone still requires a restart.
type configWatcher struct {
	registry *modelcatalog.Registry
	path     string

	appliedHash  [sha256.Size]byte
	rejectedHash *[sha256.Size]byte
}

// newConfigWatcher tracks the bytes startup actually loaded, so an edit made
// during initial discovery is picked up on the next tick.
func newConfigWatcher(
	registry *modelcatalog.Registry,
	path string,
	initialData []byte,
) *configWatcher {
	hash := sha256.Sum256(initialData)
	return &configWatcher{registry: registry, path: path, appliedHash: hash}
}

func (w *configWatcher) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *configWatcher) tick(ctx context.Context) {
	checkedAt := time.Now().UTC()
	refreshed := false
	data, err := os.ReadFile(w.path)
	hash := sha256.Sum256(data)
	switch {
	case err != nil:
		// An unreadable config is not a state to settle into quietly, so this
		// reports on every tick: the mount is what is broken.
		log.Printf("q15: config reload: %v; keeping the running providers", err)
		w.registry.SetReloadStatus(
			modelcatalog.ReloadStatus{CheckedAt: checkedAt, Error: err.Error()},
		)
	case hash == w.appliedHash:
	case w.rejectedHash != nil && hash == *w.rejectedHash:
		// Refused and reported already; wait for the file to change again.
	default:
		refreshed = w.apply(ctx, data, hash, checkedAt)
	}
	if !refreshed {
		w.registry.Refresh(ctx)
	}
}

// apply reports whether replacement refreshed the roster, so tick can avoid
// a second discovery pass.
func (w *configWatcher) apply(
	ctx context.Context,
	data []byte,
	hash [sha256.Size]byte,
	checkedAt time.Time,
) bool {
	cfg, err := config.Parse(data)
	if err != nil {
		w.refuse(hash, checkedAt, err)
		return false
	}
	providers, err := buildProviderDescriptors(cfg.Providers)
	if err != nil {
		w.refuse(hash, checkedAt, err)
		return false
	}
	if err := w.registry.ReplaceProviders(ctx, providers); err != nil {
		w.refuse(hash, checkedAt, err)
		return false
	}

	w.appliedHash = hash
	w.rejectedHash = nil
	w.registry.SetReloadStatus(modelcatalog.ReloadStatus{CheckedAt: checkedAt, Applied: true})
	log.Printf("q15: config reload applied: %d provider(s)", len(providers))
	return true
}

func (w *configWatcher) refuse(hash [sha256.Size]byte, checkedAt time.Time, err error) {
	w.rejectedHash = &hash
	log.Printf("q15: config reload: %v; keeping the running providers", err)
	w.registry.SetReloadStatus(modelcatalog.ReloadStatus{CheckedAt: checkedAt, Error: err.Error()})
}

func readConfig(path string) (config.Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("read config %q: %w", path, err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("config %q: %w", path, err)
	}
	return cfg, data, nil
}
