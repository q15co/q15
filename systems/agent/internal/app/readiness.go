package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

const runtimeReadyPath = "/tmp/q15-agent-ready"

func markRuntimeReady(path string, readyAt time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runtime readiness path is required")
	}
	if readyAt.IsZero() {
		return fmt.Errorf("runtime readiness time is required")
	}
	payload := []byte(readyAt.UTC().Format(time.RFC3339Nano) + "\n")
	if err := atomicfile.WriteBytes(path, payload); err != nil {
		return fmt.Errorf("write runtime readiness marker %q: %w", path, err)
	}
	return nil
}

func clearRuntimeReady(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runtime readiness path is required")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove runtime readiness marker %q: %w", path, err)
	}
	return nil
}
