package openaicompatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// RosterClient discovers models from an OpenAI-compatible provider via
// GET /v1/models. The list endpoint does not report capabilities, so every
// model starts Text-only; enrichment comes from models.dev.
type RosterClient struct {
	http rosterHTTPDoer
}

type rosterHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewRosterClient constructs an OpenAI-compatible model-roster discovery adapter.
func NewRosterClient(httpClient *http.Client) *RosterClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &RosterClient{http: httpClient}
}

// openaiModelsResponse is the payload of GET /v1/models.
type openaiModelsResponse struct {
	Data []openaiModelsEntry `json:"data"`
}

type openaiModelsEntry struct {
	ID string `json:"id"`
}

// Discover lists models from GET /v1/models. A non-empty base URL is required.
func (c *RosterClient) Discover(
	ctx context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return nil, errors.New("openai-compatible discovery requires base_url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	setRosterBearerAuth(req, p.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai /v1/models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai /v1/models: unexpected status %s", resp.Status)
	}

	var body openaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode openai /v1/models: %w", err)
	}

	models := make([]modelcatalog.Model, 0, len(body.Data))
	for _, entry := range body.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, modelcatalog.Model{
			ProviderModel: id,
			Name:          id,
			Capabilities:  modelcatalog.Capabilities{Text: true},
			Source:        modelcatalog.SourceOpenAI,
		})
	}
	return models, nil
}

// setRosterBearerAuth adds an Authorization: Bearer header when apiKey is non-empty.
func setRosterBearerAuth(req *http.Request, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
