// Package config loads, validates, and resolves q15 runtime configuration.
package config

import (
	"fmt"
	"os"
	"strings"
)

// lookupSecretEnvValue resolves a secret from NAME or NAME_FILE and reports
// whether either source was configured.
func lookupSecretEnvValue(name string) (string, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, fmt.Errorf("env var name is required")
	}

	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, true, nil
	}

	fileEnvName := name + "_FILE"
	filePath := strings.TrimSpace(os.Getenv(fileEnvName))
	if filePath == "" {
		return "", false, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", true, fmt.Errorf("read %q from %q: %w", fileEnvName, filePath, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", true, fmt.Errorf("%q points to an empty file %q", fileEnvName, filePath)
	}
	return value, true, nil
}

func resolveSecretEnvValue(name string) (string, error) {
	value, ok, err := lookupSecretEnvValue(name)
	if err != nil {
		return "", err
	}
	if !ok {
		name = strings.TrimSpace(name)
		return "", fmt.Errorf("env var %q or %q is required", name, name+"_FILE")
	}
	return value, nil
}
