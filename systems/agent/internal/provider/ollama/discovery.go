package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// RosterClient discovers models from an Ollama daemon via GET /api/tags and
// optionally POST /api/show per model for capabilities.
type RosterClient struct {
	http rosterHTTPDoer
}

type rosterHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewRosterClient constructs an Ollama model-roster discovery adapter.
func NewRosterClient(httpClient *http.Client) *RosterClient {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &RosterClient{http: httpClient}
}

// ollamaTagsResponse is the payload of GET /api/tags.
type ollamaTagsResponse struct {
	Models []ollamaTagsModel `json:"models"`
}

type ollamaTagsModel struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// ollamaShowResponse is the subset of POST /api/show that discovery uses.
type ollamaShowResponse struct {
	Capabilities []string           `json:"capabilities"`
	ModelInfo    map[string]float64 `json:"model_info"`
}

// Discover lists models from /api/tags, then queries /api/show per model for
// capabilities. /api/show failures are non-fatal: the affected model keeps
// Text-only capabilities.
func (c *RosterClient) Discover(
	ctx context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	base := normalizeRosterBaseURL(p.BaseURL)
	tags, err := c.fetchTags(ctx, base, p.APIKey)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/tags: %w", err)
	}

	models := make([]modelcatalog.Model, 0, len(tags))
	for _, tag := range tags {
		id := strings.TrimSpace(tag.Name)
		if id == "" {
			id = strings.TrimSpace(tag.Model)
		}
		if id == "" {
			continue
		}
		show := c.fetchShow(ctx, base, p.APIKey, id)
		models = append(models, modelcatalog.Model{
			ProviderModel:  id,
			Name:           id,
			Capabilities:   show.capabilities,
			ParameterCount: show.parameterCount,
			Source:         modelcatalog.SourceOllama,
		})
	}
	return models, nil
}

func (c *RosterClient) fetchTags(
	ctx context.Context,
	base string,
	apiKey string,
) ([]ollamaTagsModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	setRosterBearerAuth(req, apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	var body ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return body.Models, nil
}

// showResult bundles the non-fatal data extracted from a single /api/show call.
type showResult struct {
	capabilities   modelcatalog.Capabilities
	parameterCount int64
}

// fetchShow queries POST /api/show for one model and returns capabilities plus
// parameter count. Any error (network, non-200, decode) is treated as
// non-fatal and returns the Text-only default with zero parameter count.
func (c *RosterClient) fetchShow(
	ctx context.Context,
	base string,
	apiKey string,
	modelID string,
) showResult {
	payload, err := json.Marshal(ollamaShowRequest{Name: modelID})
	if err != nil {
		return showResult{capabilities: defaultRosterCapabilities()}
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		base+"/api/show",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return showResult{capabilities: defaultRosterCapabilities()}
	}
	req.Header.Set("Content-Type", "application/json")
	setRosterBearerAuth(req, apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return showResult{capabilities: defaultRosterCapabilities()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return showResult{capabilities: defaultRosterCapabilities()}
	}

	var body ollamaShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return showResult{capabilities: defaultRosterCapabilities()}
	}

	var params int64
	if v, ok := body.ModelInfo["general.parameter_count"]; ok {
		params = int64(v)
	}
	return showResult{
		capabilities:   capabilitiesFromOllamaShow(body.Capabilities),
		parameterCount: params,
	}
}

type ollamaShowRequest struct {
	Name string `json:"name"`
}

// capabilitiesFromOllamaShow maps Ollama's capability vocabulary onto the
// normalized Capabilities struct. Unknown capability names are ignored.
func capabilitiesFromOllamaShow(names []string) modelcatalog.Capabilities {
	caps := modelcatalog.Capabilities{Text: true} // completion-capable by default
	for _, raw := range names {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "completion":
			caps.Text = true
		case "tools":
			caps.ToolCalling = true
		case "vision":
			caps.ImageInput = true
		case "thinking":
			caps.Reasoning = true
		case "audio":
			caps.AudioInput = true
		}
	}
	return caps
}

func defaultRosterCapabilities() modelcatalog.Capabilities {
	return modelcatalog.Capabilities{Text: true}
}

func normalizeRosterBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(raw, "/")
}

// setRosterBearerAuth adds an Authorization: Bearer header when apiKey is non-empty.
func setRosterBearerAuth(req *http.Request, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
