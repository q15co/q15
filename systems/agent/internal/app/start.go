// Package app wires the q15 agent runtime and startup flow.
package app

import (
	"context"
	"errors"
	"fmt"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/q15co/q15/systems/agent/internal/config"
)

const (
	// ConfigPath is the fixed container path for the mounted agent config.
	ConfigPath = "/etc/q15/agent/config.yaml"
	// AuthPath is the fixed container path for the mounted auth store.
	AuthPath = "/etc/q15/auth/auth.json"
)

// Run starts the agent runtime.
func Run(ctx context.Context, args []string) error {
	if err := validateArgs(args); err != nil {
		return err
	}
	return Start(ctx)
}

// Start loads the configured agent runtime from disk and starts it.
func Start(ctx context.Context) error {
	if err := q15auth.SetStorePath(AuthPath); err != nil {
		return fmt.Errorf("configure auth store: %w", err)
	}

	runtime, err := config.LoadAgentRuntime(ConfigPath)
	if err != nil {
		return err
	}
	if runtime == nil {
		return fmt.Errorf("agent config %q does not define an agent", ConfigPath)
	}

	return runBot(ctx, *runtime)
}

func validateArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	return errors.New("q15-agent accepts no arguments")
}
