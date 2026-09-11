package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/embed"
)

// TelegramToken resolves the Telegram token from inline value or token_env.
func (a Agent) TelegramToken() (string, error) {
	token := strings.TrimSpace(a.Telegram.Token)
	if token != "" {
		return token, nil
	}

	envName := strings.TrimSpace(a.Telegram.TokenEnv)
	if envName == "" {
		return "", errors.New(
			"telegram token is required (set telegram.token or telegram.token_env)",
		)
	}

	return resolveSecretEnvValue(envName)
}

// TelegramAllowedUserIDs resolves the Telegram allow-list from inline values or
// allowed_user_ids_env. The environment source accepts comma-separated or
// whitespace-separated integer user IDs and also supports the standard
// *_FILE companion via resolveSecretEnvValue.
func (a Agent) TelegramAllowedUserIDs() ([]int64, error) {
	envName := strings.TrimSpace(a.Telegram.AllowedUserIDsEnv)
	if len(a.Telegram.AllowedUserIDs) > 0 && envName != "" {
		return nil, errors.New(
			"set either telegram.allowed_user_ids or telegram.allowed_user_ids_env, not both",
		)
	}
	if envName == "" {
		return normalizeAllowedUserIDs(a.Telegram.AllowedUserIDs)
	}

	value, err := resolveSecretEnvValue(envName)
	if err != nil {
		return nil, err
	}
	return parseAllowedUserIDs(value)
}

// BraveAPIKey resolves the optional Brave Search API key for web_search.
func (a Agent) BraveAPIKey() (string, error) {
	envName := strings.TrimSpace(a.Tools.WebSearch.BraveAPIKeyEnv)
	if envName == "" {
		return "", nil
	}

	value, ok, err := lookupSecretEnvValue(envName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("env var %q or %q is required", envName, envName+"_FILE")
	}
	return value, nil
}

// EmbeddingsRuntime resolves optional embeddings configuration. It stays
// disabled unless any embeddings field is set. Provider defaults to Gemini so
// existing configurations keep working unchanged; the openai provider targets
// any OpenAI-compatible /embeddings endpoint and may run keyless against a
// local endpoint when only base_url_env is set.
func (a Agent) EmbeddingsRuntime() (EmbeddingsToolRuntime, error) {
	tool := a.Tools.Embeddings
	if !tool.configured() {
		return EmbeddingsToolRuntime{}, nil
	}
	qdrantEnv := strings.TrimSpace(tool.QdrantURLEnv)
	if qdrantEnv == "" {
		return EmbeddingsToolRuntime{}, errors.New(
			"qdrant_url_env is required when embeddings are configured",
		)
	}
	provider, err := normalizeEmbeddingsProvider(tool.Provider)
	if err != nil {
		return EmbeddingsToolRuntime{}, err
	}
	if tool.BatchSize < 0 {
		return EmbeddingsToolRuntime{}, errors.New("batch_size must be greater than or equal to 0")
	}

	runtime := EmbeddingsToolRuntime{
		Provider:   provider,
		Model:      strings.TrimSpace(tool.Model),
		Dimensions: tool.Dimensions,
		BatchSize:  tool.BatchSize,
	}

	qdrantURL, ok, err := lookupSecretEnvValue(qdrantEnv)
	if err != nil {
		return EmbeddingsToolRuntime{}, err
	}
	if !ok {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q or %q is required",
			qdrantEnv,
			qdrantEnv+"_FILE",
		)
	}
	if strings.TrimSpace(qdrantURL) == "" {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q resolved to an empty Qdrant URL",
			qdrantEnv,
		)
	}
	runtime.QdrantURL = qdrantURL
	runtime.Enabled = true

	if provider == embed.ProviderOpenAI {
		apiKeyEnv := strings.TrimSpace(tool.APIKeyEnv)
		baseURLEnv := strings.TrimSpace(tool.BaseURLEnv)
		if apiKeyEnv == "" && baseURLEnv == "" {
			return EmbeddingsToolRuntime{}, errors.New(
				"api_key_env is required when embeddings provider is openai unless base_url_env is set",
			)
		}
		if strings.TrimSpace(tool.Model) == "" {
			return EmbeddingsToolRuntime{}, errors.New(
				"model is required when embeddings provider is openai",
			)
		}
		if tool.Dimensions <= 0 {
			return EmbeddingsToolRuntime{}, errors.New(
				"dimensions must be greater than 0 when embeddings provider is openai",
			)
		}
		if apiKeyEnv != "" {
			apiKey, err := resolveEmbeddingsSecretValue(apiKeyEnv, "API key")
			if err != nil {
				return EmbeddingsToolRuntime{}, err
			}
			runtime.APIKey = apiKey
		}
		if baseURLEnv != "" {
			baseURL, err := resolveEmbeddingsSecretValue(baseURLEnv, "base URL")
			if err != nil {
				return EmbeddingsToolRuntime{}, err
			}
			runtime.BaseURL = baseURL
		}
		return runtime, nil
	}

	geminiEnv := strings.TrimSpace(tool.GeminiAPIKeyEnv)
	if geminiEnv == "" {
		return EmbeddingsToolRuntime{}, errors.New(
			"gemini_api_key_env is required when embeddings are configured",
		)
	}
	geminiAPIKey, ok, err := lookupSecretEnvValue(geminiEnv)
	if err != nil {
		return EmbeddingsToolRuntime{}, err
	}
	if !ok {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q or %q is required",
			geminiEnv,
			geminiEnv+"_FILE",
		)
	}
	if strings.TrimSpace(geminiAPIKey) == "" {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q resolved to an empty Gemini API key",
			geminiEnv,
		)
	}
	runtime.GeminiAPIKey = geminiAPIKey
	return runtime, nil
}

// resolveEmbeddingsSecretValue looks up one embeddings env var (NAME or
// NAME_FILE) and rejects missing or empty resolutions.
func resolveEmbeddingsSecretValue(envName, what string) (string, error) {
	value, ok, err := lookupSecretEnvValue(envName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("env var %q or %q is required", envName, envName+"_FILE")
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("env var %q resolved to an empty %s", envName, what)
	}
	return value, nil
}
