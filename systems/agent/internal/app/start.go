package app

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/config"
)

//go:embed starter_config.toml
var starterConfigTemplate string

// Start loads configured agent runtimes from disk and starts them together.
func Start(ctx context.Context, configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return errors.New("config path is required")
	}

	seeded, err := ensureStarterConfig(configPath)
	if err != nil {
		return err
	}

	runtimes, err := config.LoadAgentRuntimes(configPath)
	if err != nil {
		return err
	}
	if len(runtimes) == 0 {
		if seeded {
			fmt.Printf("[app] wrote starter config: %s\n", configPath)
		}
		fmt.Printf("[app] no agents configured in %s\n", configPath)
		return nil
	}

	return runAll(ctx, runtimes)
}

func runAll(ctx context.Context, runtimes []config.AgentRuntime) error {
	if len(runtimes) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(runtimes))
	for _, rt := range runtimes {
		rt := rt
		go func() {
			if err := runBot(ctx, rt); err != nil {
				errCh <- fmt.Errorf("agent %q: %w", rt.Name, err)
				return
			}
			errCh <- nil
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err == nil {
				if ctx.Err() != nil {
					return nil
				}
				cancel()
				return errors.New("an agent runner stopped unexpectedly")
			}
			cancel()
			return err
		}
	}
}

func ensureStarterConfig(configPath string) (bool, error) {
	if _, err := os.Stat(configPath); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat config %q: %w", configPath, err)
	}

	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return false, fmt.Errorf("create config dir %q: %w", configDir, err)
	}

	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("create starter config %q: %w", configPath, err)
	}

	if _, err := file.WriteString(starterConfigTemplate); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write starter config %q: %w", configPath, err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close starter config %q: %w", configPath, err)
	}

	return true, nil
}
