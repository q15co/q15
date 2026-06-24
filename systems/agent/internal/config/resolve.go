package config

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// ResolveAgentRuntime validates the config and builds the non-model runtime
// bits (telegram, tools, exec, memory dirs). Model resolution is NOT done here
// — it is live via the modelcatalog.Registry at turn time.
func (c Config) ResolveAgentRuntime() (*AgentRuntime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Agent == nil {
		return nil, nil
	}

	agentCfg := c.Agent

	token, err := agentCfg.TelegramToken()
	if err != nil {
		return nil, fmt.Errorf("resolve telegram token for agent %q: %w", agentCfg.Name, err)
	}
	allowedUserIDs, err := agentCfg.TelegramAllowedUserIDs()
	if err != nil {
		return nil, fmt.Errorf(
			"resolve telegram allowed users for agent %q: %w",
			agentCfg.Name,
			err,
		)
	}
	braveAPIKey, err := agentCfg.BraveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("resolve brave API key for agent %q: %w", agentCfg.Name, err)
	}
	embeddingsRuntime, err := agentCfg.EmbeddingsRuntime()
	if err != nil {
		return nil, fmt.Errorf("resolve embeddings config for agent %q: %w", agentCfg.Name, err)
	}

	memoryRecentTurns := agentCfg.MemoryRecentTurns
	if memoryRecentTurns == 0 {
		memoryRecentTurns = defaultMemoryRecentTurns
	}

	return &AgentRuntime{
		Name:              strings.TrimSpace(agentCfg.Name),
		CurrentModelRef:   strings.TrimSpace(agentCfg.Model),
		WorkspaceLocalDir: runtimeWorkspaceLocalDir,
		MemoryLocalDir:    "/memory",
		MediaLocalDir:     runtimeMediaLocalDir,
		SkillsLocalDir:    runtimeSkillsLocalDir,
		MemoryRecentTurns: memoryRecentTurns,
		Execution:         ExecutionRuntime{ServiceAddress: runtimeExecutionServiceAddr},
		Tools: ToolsRuntime{
			WebSearch: WebSearchToolRuntime{
				BraveAPIKey: braveAPIKey,
			},
			Embeddings: embeddingsRuntime,
		},
		TelegramToken:          token,
		TelegramAllowedUserIDs: allowedUserIDs,
	}, nil
}

// ResolveProviderAPIKey resolves the API key for one provider based on its type.
func ResolveProviderAPIKey(provider Provider, providerType string) (string, error) {
	switch providerType {
	case providertypes.Ollama:
		if strings.TrimSpace(provider.KeyEnv) != "" {
			return resolveSecretEnvValue(provider.KeyEnv)
		}
		return "", nil
	case providertypes.OpenAICompatible:
		return resolveSecretEnvValue(provider.KeyEnv)
	case providertypes.OpenAICodex:
		return "", nil
	default:
		return "", fmt.Errorf(
			"provider %q has unsupported type %q",
			provider.Name,
			provider.Type,
		)
	}
}

// FindProvider returns the configured provider by name.
func (c Config) FindProvider(name string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.Name == name {
			return provider, true
		}
	}
	return Provider{}, false
}
