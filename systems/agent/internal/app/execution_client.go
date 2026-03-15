// Package app wires the q15 agent runtime and startup flow.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/execution"
)

const executionServiceConnectTimeout = 5 * time.Second

func connectExecutionService(
	ctx context.Context,
	runtime *config.ExecutionRuntime,
) (*execution.Client, *execpb.GetRuntimeInfoResponse, error) {
	if runtime == nil {
		return nil, nil, errors.New("execution config is required")
	}

	connectCtx, cancel := context.WithTimeout(ctx, executionServiceConnectTimeout)
	defer cancel()

	client, err := execution.NewClient(connectCtx, runtime.ServiceAddress)
	if err != nil {
		return nil, nil, err
	}

	info, err := client.GetRuntimeInfo(connectCtx)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("get runtime info: %w", err)
	}
	return client, info, nil
}
