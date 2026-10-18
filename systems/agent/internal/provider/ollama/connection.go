package ollama

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/provider/systemone"
)

type connection struct {
	base         *url.URL
	apiKey       string
	http         *http.Client
	sdk          *ollamaapi.Client
	decisionPath string
}

func newConnection(
	baseURL, apiKey, decisionPath string,
	transport http.RoundTripper,
) (connection, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return connection{}, fmt.Errorf("ollama /v1/systemone: invalid provider base URL")
	}
	if base.User != nil {
		return connection{}, fmt.Errorf(
			"ollama /v1/systemone: base URL must not contain credentials",
		)
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
	var endpoint *url.URL
	if strings.TrimSpace(decisionPath) != "" {
		endpoint, err = systemone.Endpoint(base.String(), decisionPath)
		if err != nil {
			return connection{}, err
		}
	}
	client := &http.Client{
		Transport: bearerTransport{
			target:           base,
			apiKey:           apiKey,
			next:             transport,
			decisionEndpoint: endpoint,
		},
	}
	path := "/" + strings.TrimLeft(base.JoinPath("/v1/systemone").EscapedPath(), "/")
	if endpoint != nil {
		path = endpoint.EscapedPath()
	}
	return connection{
		base:         base,
		apiKey:       apiKey,
		http:         client,
		sdk:          ollamaapi.NewClient(sdkBase, client),
		decisionPath: path,
	}, nil
}

type bearerTransport struct {
	target           *url.URL
	apiKey           string
	next             http.RoundTripper
	decisionEndpoint *url.URL
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	isDecision := strings.HasSuffix(request.URL.Path, "/v1/systemone")
	forward := request.Clone(request.Context())
	if t.apiKey != "" {
		forward.URL.Scheme = t.target.Scheme
		forward.URL.Host = t.target.Host
		forward.Host = t.target.Host
		forward.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	if t.decisionEndpoint != nil && strings.HasSuffix(forward.URL.Path, "/v1/systemone") {
		forward.URL.Path = t.decisionEndpoint.Path
		forward.URL.RawPath = t.decisionEndpoint.RawPath
	}
	response, err := t.next.RoundTrip(forward)
	if err != nil {
		return nil, err
	}
	if isDecision && (response.StatusCode < 200 || response.StatusCode >= 300) {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, ollamaapi.StatusError{StatusCode: response.StatusCode, Status: response.Status}
	}
	return response, nil
}
