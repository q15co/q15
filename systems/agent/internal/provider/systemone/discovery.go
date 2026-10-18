package systemone

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// RosterClient discovers a dedicated decision provider's native or OpenAI-compatible model list.
type RosterClient struct{ http *http.Client }

// NewRosterClient constructs a discovery adapter for System One compatible providers.
func NewRosterClient(httpClient *http.Client) *RosterClient {
	return &RosterClient{http: httpClient}
}

// Discover asks the configured API root for models without adding a version segment.
func (r *RosterClient) Discover(
	ctx context.Context,
	provider modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	client, err := NewClient(provider.BaseURL, provider.APIKey, r.http)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSuffix(client.endpoint, "systemone") + "models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("systemone /models: create request: %w", err)
	}
	if client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	resp, err := client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("systemone /models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("systemone /models: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			ReleaseDate string `json:"release_date"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("systemone /models: malformed model list")
	}
	if payload.Models == nil && payload.Data == nil {
		return nil, fmt.Errorf("systemone /models: response must contain models or data")
	}
	models := make([]modelcatalog.Model, 0, len(payload.Models)+len(payload.Data))
	for _, entry := range payload.Models {
		id := strings.TrimSpace(entry.Name)
		if id == "" {
			continue
		}
		released, _ := time.Parse(time.DateOnly, entry.ReleaseDate)
		models = append(
			models,
			modelcatalog.Model{
				ProviderModel: id,
				Name:          id,
				Capabilities: modelcatalog.Capabilities{
					Text:     true,
					Decision: true,
				}.WithProvenance(
					"derived",
				),
				ReleaseDate: released,
				Source:      modelcatalog.SourceSystemOne,
			},
		)
	}
	for _, entry := range payload.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(
			models,
			modelcatalog.Model{
				ProviderModel: id,
				Name:          id,
				Capabilities: modelcatalog.Capabilities{
					Text:     true,
					Decision: true,
				}.WithProvenance(
					"derived",
				),
				Source: modelcatalog.SourceSystemOne,
			},
		)
	}
	return models, nil
}
