package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/config"
)

func Start(ctx context.Context, configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return errors.New("config path is required")
	}

	runtimes, err := config.LoadAgentRuntimes(configPath)
	if err != nil {
		return err
	}

	return runAll(ctx, runtimes)
}

func runAll(ctx context.Context, runtimes []config.AgentRuntime) error {
	if len(runtimes) == 0 {
		return errors.New("no agents to start")
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
