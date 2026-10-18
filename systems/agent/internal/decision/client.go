package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

// Client calls a provider's /v1/systemone endpoint without logging request or response content.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// NewClient accepts a provider base URL (with or without /v1) and optional bearer key.
func NewClient(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" {
		return nil, fmt.Errorf("decision /v1/systemone: invalid provider base URL")
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
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
func (c *Client) Check(ctx context.Context, request Request) (Response, error) {
	if err := validateRequest(request); err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: %w", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: request is not JSON encodable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("decision /v1/systemone: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return Response{}, fmt.Errorf("decision /v1/systemone: response unreadable or too large")
	}
	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: malformed response JSON")
	}
	if err := validateAnswers(request.Questions, result.Answers); err != nil {
		return Response{}, fmt.Errorf("decision /v1/systemone: malformed response: %w", err)
	}
	return result, nil
}

func validateRequest(request Request) error {
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if !isState(request.State) {
		return fmt.Errorf("state must be a string, object, or array")
	}
	if len(request.Questions) < 1 || len(request.Questions) > 64 {
		return fmt.Errorf("requires 1-64 named questions")
	}
	for name, question := range request.Questions {
		if len(name) == 0 || len(name) > 100 {
			return fmt.Errorf(
				"question names require 1-100 ASCII letters, digits, underscores, dots, or hyphens",
			)
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
				return fmt.Errorf("invalid question name")
			}
		}
		if err := question.validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateAnswers(questions map[string]Question, answers map[string]Answer) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("answer names do not match questions")
	}
	for name, q := range questions {
		a, ok := answers[name]
		if !ok || a.Type != q.Type {
			return fmt.Errorf("answer name or type does not match question")
		}
		switch q.Type {
		case "noul":
			if !probability(a.Noul) {
				return fmt.Errorf("noul must be a probability")
			}
		case "choice":
			keys := make([]string, len(q.Options))
			for i, option := range q.Options {
				keys[i] = option.Name
			}
			if err := validateDistribution(a, keys); err != nil {
				return err
			}
			best := keys[0]
			for _, key := range keys[1:] {
				if a.Probabilities[key] > a.Probabilities[best] {
					best = key
				}
			}
			p, exists := a.Probabilities[a.Choice]
			if !exists || p < a.Probabilities[best] {
				return fmt.Errorf("choice must name a highest-probability option")
			}
			a.Choice = best
			answers[name] = a
		case "score":
			keys := make([]string, len(q.Levels))
			for i := range q.Levels {
				keys[i] = strconv.Itoa(i)
			}
			if err := validateDistribution(a, keys); err != nil {
				return err
			}
			if a.Score == nil || *a.Score < 0 || *a.Score > float64(len(keys)-1) ||
				len(a.Legend) != len(keys) {
				return fmt.Errorf("invalid score or legend")
			}
			for i, key := range keys {
				if a.Legend[key] != q.Levels[i] {
					return fmt.Errorf("score legend does not match levels")
				}
			}
		}
	}
	return nil
}

func validateDistribution(answer Answer, keys []string) error {
	if !probability(answer.Confidence) || len(answer.Probabilities) != len(keys) {
		return fmt.Errorf("missing confidence or option probabilities")
	}
	sum := 0.0
	for _, key := range keys {
		p, ok := answer.Probabilities[key]
		if !ok || !probability(&p) {
			return fmt.Errorf("invalid option probabilities")
		}
		sum += p
	}
	if math.Abs(sum-1) > 0.01 {
		return fmt.Errorf("option probabilities do not sum to one")
	}
	return nil
}

func probability(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1
}
