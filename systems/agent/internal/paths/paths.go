package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	ConfigFileName = "config.toml"
	AuthFileName   = "auth.json"
)

func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".config", "q15"), nil
}

func ConfigPath(configDir string) string {
	return filepath.Join(configDir, ConfigFileName)
}

func AuthPath(configDir string) string {
	return filepath.Join(configDir, AuthFileName)
}

func DefaultConfigPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return ConfigPath(dir), nil
}

func DefaultAuthPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return AuthPath(dir), nil
}
