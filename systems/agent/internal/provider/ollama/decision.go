package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/decision"
)

var _ decision.Client = (*Client)(nil)

// Check evaluates typed questions through the native SDK with a per-call deadline.
func (c *Client) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return decision.Response{}, fmt.Errorf("ollama %s: %w", c.connection.decisionPath, err)
	}
	if strings.TrimSpace(request.Model) == "" {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: model is required",
			c.connection.decisionPath,
		)
	}
	if err := request.Validate(); err != nil {
		return decision.Response{}, fmt.Errorf("ollama %s: %w", c.connection.decisionPath, err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: request is not JSON encodable", c.connection.decisionPath,
		)
	}
	var native ollamaapi.SystemOneRequest
	if err := json.Unmarshal(data, &native); err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: invalid SDK request",
			c.connection.decisionPath,
		)
	}
	response, err := c.connection.sdk.SystemOne(ctx, &native)
	if err != nil {
		return decision.Response{}, decisionSDKError(err, c.connection.decisionPath)
	}
	data, err = json.Marshal(response)
	if err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: malformed SDK response",
			c.connection.decisionPath,
		)
	}
	var result decision.Response
	if err := json.Unmarshal(data, &result); err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: malformed response JSON",
			c.connection.decisionPath,
		)
	}
	if err := result.Validate(request.Questions); err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama %s: malformed response: %w",
			c.connection.decisionPath,
			err,
		)
	}
	return result, nil
}

func decisionSDKError(err error, path string) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("ollama %s: %w", path, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("ollama %s: %w", path, context.DeadlineExceeded)
	}
	var status ollamaapi.StatusError
	if errors.As(err, &status) {
		return fmt.Errorf("ollama %s: HTTP %d", path, status.StatusCode)
	}
	// SDK parse and upstream errors may include response content.
	return fmt.Errorf("ollama %s: SDK request or response failed", path)
}

// ContractVersion identifies the implemented decision port.
func (c *Client) ContractVersion() string { return decision.ContractVersion }
