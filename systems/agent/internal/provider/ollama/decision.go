package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/decision"
)

type decisionClient struct{ sdk *ollamaapi.Client }

var _ decision.Client = (*decisionClient)(nil)

// NewDecisionClient constructs Ollama's decision adapter with the native API root.
func NewDecisionClient(
	baseURL, apiKey string,
	transport http.RoundTripper,
) (decision.Client, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if base.User != nil {
		return nil, fmt.Errorf("ollama /v1/systemone: base URL must not contain credentials")
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	apiKey = strings.TrimSpace(apiKey)
	sdkBase := cloneURL(base)
	if apiKey != "" {
		// The SDK forces SSH-key auth for ollama.com before calling its transport.
		// This internal authority lets the transport use the configured bearer key instead.
		sdkBase.Host = "q15-ollama.invalid"
	}
	client := &http.Client{
		Transport: decisionSDKTransport{target: base, apiKey: apiKey, next: transport},
		Timeout:   120 * time.Second,
	}
	return &decisionClient{sdk: ollamaapi.NewClient(sdkBase, client)}, nil
}

func (c *decisionClient) Check(
	ctx context.Context,
	request decision.Request,
) (decision.Response, error) {
	if err := ctx.Err(); err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: %w", err)
	}
	if strings.TrimSpace(request.Model) == "" {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: model is required")
	}
	if err := request.Validate(); err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: %w", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return decision.Response{}, fmt.Errorf(
			"ollama /v1/systemone: request is not JSON encodable",
		)
	}
	var native ollamaapi.SystemOneRequest
	if err := json.Unmarshal(data, &native); err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: invalid SDK request")
	}
	response, err := c.sdk.SystemOne(ctx, &native)
	if err != nil {
		return decision.Response{}, decisionSDKError(err)
	}
	data, err = json.Marshal(response)
	if err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: malformed SDK response")
	}
	var result decision.Response
	if err := json.Unmarshal(data, &result); err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: malformed response JSON")
	}
	if err := result.Validate(request.Questions); err != nil {
		return decision.Response{}, fmt.Errorf("ollama /v1/systemone: malformed response: %w", err)
	}
	return result, nil
}

type decisionSDKTransport struct {
	target *url.URL
	apiKey string
	next   http.RoundTripper
}

func (t decisionSDKTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	forward := request.Clone(request.Context())
	if t.apiKey != "" {
		forward.URL.Scheme = t.target.Scheme
		forward.URL.Host = t.target.Host
		forward.Host = t.target.Host
		forward.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	response, err := t.next.RoundTrip(forward)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ollamaapi.StatusError{StatusCode: response.StatusCode, Status: response.Status}
	}
	return response, nil
}

func decisionSDKError(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("ollama /v1/systemone: %w", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("ollama /v1/systemone: %w", context.DeadlineExceeded)
	}
	var status ollamaapi.StatusError
	if errors.As(err, &status) {
		return fmt.Errorf("ollama /v1/systemone: HTTP %d", status.StatusCode)
	}
	// SDK parse and upstream errors may include response content.
	return fmt.Errorf("ollama /v1/systemone: SDK request or response failed")
}
