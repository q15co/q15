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

// Start loads the configured agent runtime from disk and starts it.
func Start(ctx context.Context, configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return errors.New("config path is required")
	}

	seeded, err := ensureStarterConfig(configPath)
	if err != nil {
		return err
	}

	runtime, err := config.LoadAgentRuntime(configPath)
	if err != nil {
		return err
	}
	if runtime == nil {
		if seeded {
			fmt.Printf("[app] wrote starter config: %s\n", configPath)
		}
		fmt.Printf("[app] no agent configured in %s\n", configPath)
		return nil
	}

	return runBot(ctx, *runtime)
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
