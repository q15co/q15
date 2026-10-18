package openaicompatible

import (
	"net/http"
	"time"

	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/provider/systemone"
)

// NewDecisionClient constructs a System One adapter at the configured provider API root.
func NewDecisionClient(
	baseURL, apiKey string,
	transport http.RoundTripper,
) (decision.Client, error) {
	return systemone.NewClient(
		baseURL,
		apiKey,
		&http.Client{Transport: transport, Timeout: 120 * time.Second},
	)
}
