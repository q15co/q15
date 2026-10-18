package openaicompatible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type teiInfo struct {
	ModelID         string `json:"model_id"`
	ServedModelName string `json:"served_model_name"`
	MaxInputLength  int    `json:"max_input_length"`
	ModelType       struct {
		Embedding *struct{} `json:"embedding"`
	} `json:"model_type"`
}

// TEI serves OpenAI-compatible embeddings without a /models endpoint. Its /info
// describes the single loaded model; served_model_name is the request identity.
func (c *RosterClient) discoverTEI(
	ctx context.Context,
	p modelcatalog.Provider,
	base string,
) ([]modelcatalog.Model, error) {
	endpoint := base + "/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("TEI info at %s: %w", endpoint, err)
	}
	setRosterBearerAuth(req, p.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TEI info at %s: %w", req.URL.Redacted(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"openai models unavailable; TEI info at %s: unexpected status %s; check base_url against the provider's documented API root",
			req.URL.Redacted(),
			resp.Status,
		)
	}
	var info teiInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode TEI info at %s: %w", req.URL.Redacted(), err)
	}
	if strings.TrimSpace(info.ModelID) == "" || info.ModelType.Embedding == nil ||
		info.MaxInputLength < 0 {
		return nil, fmt.Errorf(
			"TEI info at %s: missing or invalid embedding model metadata",
			req.URL.Redacted(),
		)
	}
	id := strings.TrimSpace(info.ServedModelName)
	if id == "" {
		id = strings.TrimSpace(info.ModelID)
	}
	caps := modelcatalog.Capabilities{
		Known: true,
		Text:  true,
		Embed: true,
	}.WithProvenance(
		"derived",
	)
	caps.Set("chat", false, "derived")
	caps.Set("decision", false, "derived")
	return []modelcatalog.Model{{
		ProviderModel:    id,
		Name:             id,
		Capabilities:     caps,
		MaxContextTokens: info.MaxInputLength,
		Source:           modelcatalog.SourceOpenAI,
	}}, nil
}
