package ollama

import (
	"net/http"
	"time"

	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/provider/systemone"
)

// NewDecisionClient constructs Ollama's decision adapter with the native API root.
func NewDecisionClient(
	baseURL, apiKey string,
	transport http.RoundTripper,
) (decision.Client, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	// The pinned Ollama Go SDK predates SystemOne. Keep the HTTP fallback behind this provider boundary.
	return systemone.NewClient(
		base.String()+"/v1",
		apiKey,
		&http.Client{Transport: transport, Timeout: 120 * time.Second},
	)
}
