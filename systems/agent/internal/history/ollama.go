package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/provider/ollama"
)

// NewOllamaDigester binds an explicit native Ollama model without fallback.
func NewOllamaDigester(baseURL, apiKey, provider, model string) (DigestGenerator, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
		base.User != nil ||
		base.RawQuery != "" ||
		base.Fragment != "" ||
		provider == "" ||
		model == "" {
		return nil, errors.New(
			"history summaries require an explicit Ollama endpoint, provider and model",
		)
	}
	if base.Host == "ollama.com" && (base.Scheme != "https" || strings.TrimSpace(apiKey) == "") {
		return nil, errors.New("ollama Cloud summaries require HTTPS and a subscription credential")
	}
	client, err := ollama.NewClient(baseURL, apiKey, nil, nil, "")
	if err != nil {
		return nil, err
	}
	client = client.WithJSONOutput(2048)
	return DigestFunc(func(ctx context.Context, source string) (DigestResult, error) {
		result, err := client.Complete(
			ctx,
			model,
			[]conversation.Message{
				conversation.SystemMessage(DigestSystemPrompt),
				conversation.UserMessage(source),
			},
			nil,
		)
		if err != nil {
			return DigestResult{}, errors.New("ollama summary request failed")
		}
		var body strings.Builder
		for _, message := range result.Messages {
			for _, part := range message.Parts {
				if part.Type == conversation.TextPartType {
					body.WriteString(part.Text)
				}
			}
		}
		var output struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(body.String()), &output); err != nil {
			return DigestResult{}, errors.New("summary response is not valid JSON")
		}
		return DigestResult{
			Body:         output.Summary,
			Provider:     provider,
			Model:        model,
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		}, nil
	}), nil
}
