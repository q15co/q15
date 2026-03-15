// Package paths provides filesystem path helpers for q15 agent auth state.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// AuthFileName is the default auth store filename within the config directory.
	AuthFileName = "auth.json"
)

// DefaultConfigDir returns the default per-user q15 config directory.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".config", "q15"), nil
}

// AuthPath returns the auth store path within the given config directory.
func AuthPath(configDir string) string {
	return filepath.Join(configDir, AuthFileName)
}

// DefaultAuthPath returns the default per-user q15 auth store path.
func DefaultAuthPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return AuthPath(dir), nil
}
