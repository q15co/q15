// Package systemone implements the System One compatible HTTP protocol.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/decision"
)

const maxResponseBytes = 8 << 20

// Client calls a provider's /v1/systemone endpoint without logging request or response content.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

var _ decision.Client = (*Client)(nil)

// NewClient accepts the documented API root and an optional bearer key.
// The relative path systemone is joined without guessing a version segment.
func NewClient(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" {
		return nil, fmt.Errorf("decision /v1/systemone: invalid provider base URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{
		endpoint: base + "/systemone",
		apiKey:   strings.TrimSpace(apiKey),
		http:     httpClient,
	}, nil
}

// Check validates a request and returns one typed answer for each named question.
func (c *Client) Check(ctx context.Context, request decision.Request) (decision.Response, error) {
	if strings.TrimSpace(request.Model) == "" {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: model is required")
	}
	if err := request.Validate(); err != nil {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: %w", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return decision.Response{}, fmt.Errorf(
			"decision /v1/systemone: request is not JSON encodable",
		)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return decision.Response{}, fmt.Errorf(
			"decision /v1/systemone: response unreadable or too large",
		)
	}
	var result decision.Response
	if err := json.Unmarshal(body, &result); err != nil {
		return decision.Response{}, fmt.Errorf("decision /v1/systemone: malformed response JSON")
	}
	if err := result.Validate(request.Questions); err != nil {
		return decision.Response{}, fmt.Errorf(
			"decision /v1/systemone: malformed response: %w",
			err,
		)
	}
	return result, nil
}

// ContractVersion identifies the implemented decision port.
func (c *Client) ContractVersion() string { return decision.ContractVersion }
