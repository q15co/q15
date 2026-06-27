package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// Validate checks that the config is internally consistent.
func (c Config) Validate() error {
	return c.validate()
}

func (c Config) validate() error {
	if c.Agent != nil && len(c.Providers) == 0 {
		return errors.New("providers cannot be empty when agent is configured")
	}

	providers := make(map[string]struct{}, len(c.Providers))
	for i, provider := range c.Providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if _, ok := providers[name]; ok {
			return fmt.Errorf("duplicate provider name %q", name)
		}
		providers[name] = struct{}{}

		if strings.TrimSpace(provider.Type) == "" {
			return fmt.Errorf("providers[%d].type is required", i)
		}
		normalizedType := providertypes.MustNormalize(provider.Type)
		if normalizedType == "" {
			return fmt.Errorf("providers[%d].type %q is not supported", i, provider.Type)
		}
		if normalizedType == providertypes.OpenAICompatible &&
			strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf(
				"providers[%d].base_url is required for openai-compatible providers",
				i,
			)
		}
		if normalizedType == providertypes.OpenAICompatible &&
			strings.TrimSpace(provider.KeyEnv) == "" {
			return fmt.Errorf("providers[%d].key_env is required", i)
		}
		if normalizedType == providertypes.Ollama && strings.TrimSpace(provider.KeyEnv) == "" &&
			isOllamaCloudBaseURL(provider.BaseURL) {
			return fmt.Errorf(
				"providers[%d].key_env is required for Ollama Cloud providers",
				i,
			)
		}
		if err := validateDiscoveryGlobs(i, provider.Discovery); err != nil {
			return err
		}
	}

	if c.Agent == nil {
		return nil
	}

	name := strings.TrimSpace(c.Agent.Name)
	if name == "" {
		return errors.New("agent.name is required")
	}

	if strings.TrimSpace(c.Agent.Model) == "" {
		return errors.New("agent.model is required (the current model ref)")
	}

	if strings.TrimSpace(c.Agent.Telegram.Token) == "" &&
		strings.TrimSpace(c.Agent.Telegram.TokenEnv) == "" {
		return errors.New("agent.telegram requires token or token_env")
	}
	allowedUserIDsEnv := strings.TrimSpace(c.Agent.Telegram.AllowedUserIDsEnv)
	if len(c.Agent.Telegram.AllowedUserIDs) == 0 && allowedUserIDsEnv == "" {
		return errors.New("agent.telegram requires allowed_user_ids or allowed_user_ids_env")
	}
	if len(c.Agent.Telegram.AllowedUserIDs) > 0 && allowedUserIDsEnv != "" {
		return errors.New(
			"agent.telegram.allowed_user_ids and agent.telegram.allowed_user_ids_env are mutually exclusive",
		)
	}
	if len(c.Agent.Telegram.AllowedUserIDs) > 0 {
		if _, err := normalizeAllowedUserIDs(c.Agent.Telegram.AllowedUserIDs); err != nil {
			return fmt.Errorf("agent.telegram.allowed_user_ids: %w", err)
		}
	}
	if err := c.Agent.Tools.Embeddings.validate(); err != nil {
		return fmt.Errorf("agent.tools.embeddings: %w", err)
	}

	return nil
}

func (e EmbeddingsTool) configured() bool {
	return strings.TrimSpace(e.QdrantURLEnv) != "" ||
		strings.TrimSpace(e.GeminiAPIKeyEnv) != "" ||
		strings.TrimSpace(e.Model) != "" ||
		e.Dimensions != 0
}

func (e EmbeddingsTool) validate() error {
	if !e.configured() {
		return nil
	}
	if strings.TrimSpace(e.QdrantURLEnv) == "" {
		return errors.New("qdrant_url_env is required when embeddings are configured")
	}
	if strings.TrimSpace(e.GeminiAPIKeyEnv) == "" {
		return errors.New("gemini_api_key_env is required when embeddings are configured")
	}
	if e.Dimensions < 0 {
		return errors.New("dimensions must be greater than or equal to 0")
	}
	return nil
}

func isOllamaCloudBaseURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	base, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(base.Hostname(), "ollama.com")
}

func normalizeAllowedUserIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, errors.New("must contain at least one user id")
	}

	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("[%d] must be greater than 0", i)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("[%d] duplicates user id %d", i, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	return out, nil
}

// validateDiscoveryGlobs checks that include/exclude glob patterns are valid
// path.Match syntax.
func validateDiscoveryGlobs(providerIndex int, d ProviderDiscovery) error {
	for j, pattern := range d.Include {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf(
				"providers[%d].discovery.include[%d] %q is not a valid glob: %w",
				providerIndex,
				j,
				pattern,
				err,
			)
		}
	}
	for j, pattern := range d.Exclude {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf(
				"providers[%d].discovery.exclude[%d] %q is not a valid glob: %w",
				providerIndex,
				j,
				pattern,
				err,
			)
		}
	}
	return nil
}

func parseAllowedUserIDs(value string) ([]int64, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(fields) == 0 {
		return nil, errors.New("must contain at least one user id")
	}

	ids := make([]int64, 0, len(fields))
	for i, field := range fields {
		id, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("[%d] %q is not a valid int64", i, field)
		}
		ids = append(ids, id)
	}

	return normalizeAllowedUserIDs(ids)
}
