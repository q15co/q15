package openaicompatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// RosterClient discovers models from an OpenAI-compatible provider by asking
// its API root for the model list (GET {base_url}/models). The list endpoint
// does not report capabilities, so every model starts Text-only; enrichment
// comes from models.dev.
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

// openaiModelsResponse is the payload of GET {base_url}/models.
type openaiModelsResponse struct {
	Data []openaiModelsEntry `json:"data"`
}

type openaiModelsEntry struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Metadata struct {
		Tags []string `json:"tags"`
	} `json:"metadata"`
}

// Discover lists the provider's models from GET {base_url}/models. A non-empty
// base URL is required. base_url is the provider's documented API root
// (for example https://api.moonshot.ai/v1 or https://api.deepseek.com); the relative path `models`
// is joined onto it, exactly as the chat client joins `chat/completions`, so no
// version segment is appended here.
func (c *RosterClient) Discover(
	ctx context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return nil, errors.New("openai-compatible discovery requires base_url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	setRosterBearerAuth(req, p.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"openai models: unexpected status %s at %s; check base_url against the provider's documented API root (including its path)",
			resp.Status,
			req.URL.Redacted(),
		)
	}

	var body openaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode openai models: %w", err)
	}

	models := make([]modelcatalog.Model, 0, len(body.Data))
	for _, entry := range body.Data {
		// DeepInfra classifies its mixed roster with tags. Standard OpenAI
		// rosters omit them, so retain unclassified entries.
		if nonChatModel(entry.Metadata.Tags) {
			continue
		}
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, modelcatalog.Model{
			ProviderModel: id,
			Name:          id,
			Capabilities: modelcatalog.Capabilities{
				Text:     true,
				Chat:     entry.Type != "decision",
				Decision: entry.Type == "decision",
			}.WithProvenance(
				"derived",
			),
			Source: modelcatalog.SourceOpenAI,
		})
	}
	return models, nil
}

func nonChatModel(tags []string) bool {
	if slices.Contains(tags, "chat") {
		return false
	}
	return slices.ContainsFunc(tags, func(tag string) bool {
		switch tag {
		case "embed", "image-gen", "video-gen", "tts", "stt":
			return true
		}
		return false
	})
}

// setRosterBearerAuth adds an Authorization: Bearer header when apiKey is non-empty.
func setRosterBearerAuth(req *http.Request, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
