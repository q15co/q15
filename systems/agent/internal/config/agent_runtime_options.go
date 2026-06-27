package config

import (
	"errors"
	"fmt"
	"strings"
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
// disabled unless both the Qdrant URL and Gemini API key env names are set.
// EmbeddingsRuntime resolves optional embeddings configuration. It stays
// disabled unless both the Qdrant URL and Gemini API key env names are set.
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
	geminiEnv := strings.TrimSpace(tool.GeminiAPIKeyEnv)
	if geminiEnv == "" {
		return EmbeddingsToolRuntime{}, errors.New(
			"gemini_api_key_env is required when embeddings are configured",
		)
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
	if strings.TrimSpace(qdrantURL) == "" {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q resolved to an empty Qdrant URL",
			qdrantEnv,
		)
	}
	if strings.TrimSpace(geminiAPIKey) == "" {
		return EmbeddingsToolRuntime{}, fmt.Errorf(
			"env var %q resolved to an empty Gemini API key",
			geminiEnv,
		)
	}
	return EmbeddingsToolRuntime{
		Enabled:      true,
		QdrantURL:    qdrantURL,
		GeminiAPIKey: geminiAPIKey,
		Model:        strings.TrimSpace(tool.Model),
		Dimensions:   tool.Dimensions,
	}, nil
}
