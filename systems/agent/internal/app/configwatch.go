package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// configWatcher applies an edited agent config to the running registry without a
// restart.
//
// The agent reads its config once at startup, so an operator edit used to take
// effect only on restart: a corrected provider base_url kept answering 404 until
// somebody restarted the container. The roster ticker is already there and
// already pays for one HTTP round trip per provider, so a hash of the config file
// beside it costs nothing worth measuring.
//
// Only the config file is hashed. Secrets are re-resolved whenever a reload
// actually runs, but a rotated secret file on its own is not a change to this
// file, so it still needs a restart; the alternative is re-reading every secret
// every ten minutes to learn what the hash already said could not have changed.
//
// An edit is applied whole or not at all: it must load, its providers must
// resolve, and they must discover at least one model between them. A refused edit
// leaves the running providers untouched, is logged once, and is reported through
// modelcatalog.ReloadStatus so that an operator can see why nothing changed.
type configWatcher struct {
	registry *modelcatalog.Registry
	path     string

	// applied is the hash of the config currently in effect. current is the
	// provider list it resolved to, kept unredacted so that a refused edit can be
	// rolled back without another pass over the secrets.
	applied []byte
	current []modelcatalog.Provider

	// rejected is the hash of a config that failed to apply, so the same bad file
	// is neither retried nor re-reported on every tick.
	rejected []byte
}

// newConfigWatcher returns a watcher for the config at path, whose providers are
// already the ones in effect in registry.
func newConfigWatcher(
	registry *modelcatalog.Registry,
	path string,
	providers []modelcatalog.Provider,
) (*configWatcher, error) {
	hash, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	return &configWatcher{
		registry: registry,
		path:     path,
		applied:  hash,
		current:  providers,
	}, nil
}

// run re-reads the config on the roster interval until ctx is done. It never
// returns an error.
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

// tick applies an edited config when the file changed, then refreshes the roster.
func (w *configWatcher) tick(ctx context.Context) {
	checkedAt := time.Now().UTC()
	refreshed := false
	switch hash, err := hashFile(w.path); {
	case err != nil:
		// An unreadable config is not a state to settle into quietly, so this
		// reports on every tick: the mount is what is broken.
		log.Printf("q15: config reload: %v; keeping the running providers", err)
		w.registry.SetReloadStatus(
			modelcatalog.ReloadStatus{CheckedAt: checkedAt, Err: err.Error()},
		)
	case bytes.Equal(hash, w.applied):
		// Unchanged: nothing to apply.
	case bytes.Equal(hash, w.rejected):
		// Refused and reported already; wait for the file to change again.
	default:
		refreshed = w.apply(ctx, hash, checkedAt)
	}
	if !refreshed {
		w.registry.Refresh(ctx)
	}
}

// apply loads the edited config and hands its providers to the registry, or
// leaves everything as it was and records why. It reports whether the roster has
// already been refreshed against the providers now in effect, so that a tick does
// not discover the same providers twice.
func (w *configWatcher) apply(ctx context.Context, hash []byte, at time.Time) bool {
	providers, err := loadProviders(w.path)
	if err != nil {
		w.refuse(hash, at, err)
		return false
	}
	if err := w.registry.SetProviders(providers); err != nil {
		w.refuse(hash, at, err)
		return false
	}

	w.registry.Refresh(ctx)
	if w.registry.IsEmpty() {
		// An edit that leaves nothing to discover is refused, the same way
		// startup refuses to run with an empty roster. Put the working providers
		// back before reporting it.
		if restoreErr := w.registry.SetProviders(w.current); restoreErr != nil {
			w.refuse(hash, at, fmt.Errorf(
				"the edited providers discovered no models, and restoring the running ones failed: %w",
				restoreErr,
			))
			return true
		}
		w.registry.Refresh(ctx)
		w.refuse(hash, at, errors.New("the edited providers discovered no models"))
		return true
	}

	w.applied = hash
	w.rejected = nil
	w.current = providers
	w.registry.SetReloadStatus(modelcatalog.ReloadStatus{CheckedAt: at, Applied: true})
	log.Printf("q15: config reload applied: %d provider(s)", len(providers))
	return true
}

// refuse records a rejected edit. The running providers stay in place, and the
// reason goes to the log once and to the reload status for the tool surface.
func (w *configWatcher) refuse(hash []byte, at time.Time, err error) {
	w.rejected = hash
	log.Printf("q15: config reload: %v; keeping the running providers", err)
	w.registry.SetReloadStatus(modelcatalog.ReloadStatus{CheckedAt: at, Err: err.Error()})
}

// watchConfig keeps the roster live and applies an edited provider config on the
// roster interval. It never returns.
func watchConfig(
	ctx context.Context,
	registry *modelcatalog.Registry,
	path string,
	providers []modelcatalog.Provider,
	interval time.Duration,
) {
	watcher, err := newConfigWatcher(registry, path, providers)
	if err != nil {
		// Without a readable config there is nothing to compare against, but the
		// roster still has to stay live.
		log.Printf("q15: config reload disabled: %v", err)
		_ = registry.Run(ctx)
		return
	}
	watcher.run(ctx, interval)
}

// loadProviders reads and validates the config at path and resolves its provider
// API keys, exactly as startup does.
func loadProviders(path string) ([]modelcatalog.Provider, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return buildProviderDescriptors(cfg.Providers)
}

// hashFile returns the sha256 of the file at path.
func hashFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}
