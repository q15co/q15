// Package config loads, validates, and resolves q15 proxy-service configuration.
package config

import (
	"fmt"
	"os"
	"strings"
)

func resolveSecretEnvValue(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("env var name is required")
	}

	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}

	fileEnvName := name + "_FILE"
	filePath := strings.TrimSpace(os.Getenv(fileEnvName))
	if filePath == "" {
		return "", fmt.Errorf("env var %q or %q is required", name, fileEnvName)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read %q from %q: %w", fileEnvName, filePath, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%q points to an empty file %q", fileEnvName, filePath)
	}
	return value, nil
}
