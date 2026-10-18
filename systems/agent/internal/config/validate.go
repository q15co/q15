package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/provider/systemone"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// Validate checks that the config is internally consistent.
func (c Config) Validate() error {
	return c.validate()
}

func (c Config) validate() error {
	if err := c.validateSchemaVersion(); err != nil {
		return err
	}

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
		normalizedType := providertypes.NormalizeOrEmpty(provider.Type)
		if normalizedType == "" {
			return fmt.Errorf("providers[%d].type %q is not supported", i, provider.Type)
		}
		if (normalizedType == providertypes.OpenAICompatible || normalizedType == providertypes.SystemOneCompatible) &&
			strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf(
				"providers[%d].base_url is required for %s providers",
				i, normalizedType,
			)
		}
		if normalizedType == providertypes.OpenAICompatible ||
			normalizedType == providertypes.SystemOneCompatible {
			if err := validateOpenAICompatibleBaseURL(provider.BaseURL); err != nil {
				return fmt.Errorf("providers[%d].base_url: %w", i, err)
			}
		}
		if normalizedType == providertypes.OpenAICompatible &&
			strings.TrimSpace(provider.KeyEnv) == "" && !isLocalBaseURL(provider.BaseURL) {
			return fmt.Errorf(
				"providers[%d].key_env is required for openai-compatible providers on a public host; set key_env or point base_url at a local endpoint",
				i,
			)
		}
		if normalizedType == providertypes.SystemOneCompatible {
			base, err := url.Parse(strings.TrimSpace(provider.BaseURL))
			if err != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
				return fmt.Errorf(
					"providers[%d].base_url must not include credentials, a query, or a fragment",
					i,
				)
			}
		}
		if normalizedType == providertypes.Ollama && strings.TrimSpace(provider.KeyEnv) == "" &&
			isOllamaCloudBaseURL(provider.BaseURL) {
			return fmt.Errorf(
				"providers[%d].key_env is required for Ollama Cloud providers",
				i,
			)
		}
		if strings.TrimSpace(provider.DecisionPath) != "" {
			base := provider.BaseURL
			if strings.TrimSpace(base) == "" && normalizedType == providertypes.Ollama {
				base = "http://localhost:11434"
			}
			if _, err := systemone.Endpoint(base, provider.DecisionPath); err != nil {
				return fmt.Errorf("providers[%d].decision_path: %w", i, err)
			}
		}
		for model, capabilities := range provider.CapabilityOverrides {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("providers[%d].capability_overrides requires a model ID", i)
			}
			for capability := range capabilities {
				switch capability {
				case "chat",
					"embed",
					"decision",
					"text",
					"image_input",
					"audio_input",
					"tool_calling",
					"reasoning":
				default:
					return fmt.Errorf(
						"providers[%d].capability_overrides model %q: unsupported capability %q",
						i,
						model,
						capability,
					)
				}
			}
		}
		if err := validateDiscoveryGlobs(i, provider.Discovery); err != nil {
			return err
		}
	}

	if err := c.validateRoleShapes(); err != nil {
		return err
	}
	if c.Agent == nil {
		return nil
	}

	name := strings.TrimSpace(c.Agent.Name)
	if name == "" {
		return errors.New("agent.name is required")
	}

	if err := validateTelegram(c.Agent.Telegram); err != nil {
		return err
	}
	if err := c.Agent.Tools.Embeddings.validate(); err != nil {
		return fmt.Errorf("agent.tools.embeddings: %w", err)
	}
	if err := c.Agent.Tools.Schedule.validate(); err != nil {
		return fmt.Errorf("agent.tools.schedule: %w", err)
	}
	if err := validateMemoryBudget(
		"memory_working_budget_bytes",
		c.Agent.MemoryWorkingBudgetBytes,
	); err != nil {
		return err
	}
	if err := validateMemoryBudget(
		"memory_open_loops_budget_bytes",
		c.Agent.MemoryOpenLoopsBudgetBytes,
	); err != nil {
		return err
	}
	if err := validateBridgeListenTarget(c.Agent.Bridge.ListenTarget); err != nil {
		return fmt.Errorf("agent.bridge.listen_target: %w", err)
	}

	return nil
}

// checkSchemaVersion reports whether a configuration that declares `have` is
// readable by a build whose schema version is `want`. An absent version (0)
// means the configuration predates the field, and stays legal forever: it is
// read as the current version. Any other mismatch is refused with the
// direction named, so a configuration is never read or upgraded silently.
//
// The check covers builds that know the field. What stops an older build from
// receiving a newer configuration is the loader's strict field check, which
// fails the decode outright rather than reporting a version mismatch: this
// check is the clear message for every build from here on. A future schema
// version adds its migration step here rather than loosening the check.
func checkSchemaVersion(have, want int) error {
	switch {
	case have == 0 || have == want:
		return nil
	case have > want:
		return fmt.Errorf(
			"config schema version %d is newer than this build reads (highest %d)",
			have,
			want,
		)
	case have < 0:
		return fmt.Errorf("config schema version %d is not a valid version", have)
	default:
		return fmt.Errorf(
			"config schema version %d is older than this build reads (%d): migrate the configuration before loading it",
			have,
			want,
		)
	}
}

func (c Config) validateSchemaVersion() error {
	return checkSchemaVersion(c.SchemaVersion, SchemaVersion)
}

// validateTelegram validates the Telegram transport, which is optional: the
// transport is configured exactly when the agent names a token source, and an
// agent reachable only through the chat bridge names none. A half-configured
// transport is still an error, in both directions, because a token and an
// allow-list that disagree are otherwise silent until the transport starts.
func validateTelegram(tg Telegram) error {
	token := strings.TrimSpace(tg.Token)
	tokenEnv := strings.TrimSpace(tg.TokenEnv)
	allowedUserIDsEnv := strings.TrimSpace(tg.AllowedUserIDsEnv)

	if token == "" && tokenEnv == "" {
		if len(tg.AllowedUserIDs) > 0 || allowedUserIDsEnv != "" {
			return errors.New(
				"agent.telegram allow-list is set without a token: set telegram.token or telegram.token_env, or drop allowed_user_ids and allowed_user_ids_env",
			)
		}
		return nil
	}
	if len(tg.AllowedUserIDs) == 0 && allowedUserIDsEnv == "" {
		return errors.New("agent.telegram requires allowed_user_ids or allowed_user_ids_env")
	}
	if len(tg.AllowedUserIDs) > 0 && allowedUserIDsEnv != "" {
		return errors.New(
			"agent.telegram.allowed_user_ids and agent.telegram.allowed_user_ids_env are mutually exclusive",
		)
	}
	if len(tg.AllowedUserIDs) > 0 {
		if _, err := normalizeAllowedUserIDs(tg.AllowedUserIDs); err != nil {
			return fmt.Errorf("agent.telegram.allowed_user_ids: %w", err)
		}
	}
	return nil
}

// validateMemoryBudget rejects a negative memory byte budget. Zero stays
// legal: it is how an absent key arrives, and ResolveAgentRuntime applies the
// documented default for it, so only an explicitly configured budget has to
// name a usable size.
func validateMemoryBudget(key string, value int) error {
	if value < 0 {
		return fmt.Errorf("agent.%s must not be negative", key)
	}
	return nil
}

// validateBridgeListenTarget mirrors the bridge resolver's unix-only rule, so
// a bad value fails at config load instead of at the listener's bind: the
// bridge serves the identity surface and accepts only a unix:// socket, and
// config must not promise the host:port form that resolver refuses at
// startup. An empty value stays legal: it disables the listener.
func validateBridgeListenTarget(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, "unix://") {
		return fmt.Errorf(
			"%q must use the unix:// scheme; the chat bridge is the identity "+
				"surface and is never served over tcp",
			value,
		)
	}
	return nil
}

func (e EmbeddingsTool) configured() bool {
	return strings.TrimSpace(e.QdrantURLEnv) != "" ||
		strings.TrimSpace(e.QdrantAPIKeyEnv) != "" ||
		strings.TrimSpace(e.Provider) != "" ||
		strings.TrimSpace(e.GeminiAPIKeyEnv) != "" ||
		strings.TrimSpace(e.APIKeyEnv) != "" ||
		strings.TrimSpace(e.BaseURLEnv) != "" ||
		strings.TrimSpace(e.Model) != "" ||
		e.Dimensions != 0 ||
		e.BatchSize != 0
}

func (e EmbeddingsTool) validate() error {
	if !e.configured() {
		return nil
	}
	if strings.TrimSpace(e.QdrantURLEnv) == "" {
		return errors.New("qdrant_url_env is required when embeddings are configured")
	}
	provider, err := normalizeEmbeddingsProvider(e.Provider)
	if err != nil {
		return err
	}
	switch provider {
	case embed.ProviderOpenAI:
		if strings.TrimSpace(e.APIKeyEnv) == "" && strings.TrimSpace(e.BaseURLEnv) == "" {
			return errors.New(
				"api_key_env is required when embeddings provider is openai unless base_url_env is set",
			)
		}
		if strings.TrimSpace(e.Model) == "" {
			return errors.New("model is required when embeddings provider is openai")
		}
		if e.Dimensions <= 0 {
			return errors.New(
				"dimensions must be greater than 0 when embeddings provider is openai",
			)
		}
	default:
		if strings.TrimSpace(e.GeminiAPIKeyEnv) == "" {
			return errors.New("gemini_api_key_env is required when embeddings are configured")
		}
	}
	if e.Dimensions < 0 {
		return errors.New("dimensions must be greater than or equal to 0")
	}
	if e.BatchSize < 0 {
		return errors.New("batch_size must be greater than or equal to 0")
	}
	return nil
}

// normalizeEmbeddingsProvider resolves the configured embeddings provider,
// defaulting to the Gemini default when unset. It rejects unknown providers.
func normalizeEmbeddingsProvider(provider string) (string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return embed.DefaultProvider, nil
	}
	switch provider {
	case embed.ProviderGemini, embed.ProviderOpenAI:
		return provider, nil
	default:
		return "", fmt.Errorf("embedding provider %q is not supported", provider)
	}
}

func (s ScheduleTool) validate() error {
	if s.MaxJobs < 0 || s.MaxJobs > maxScheduleJobs {
		return fmt.Errorf("max_jobs must be between 1 and %d when configured", maxScheduleJobs)
	}
	if s.MaxRunTurns < 0 || s.MaxRunTurns > maxScheduleRunTurns {
		return fmt.Errorf(
			"max_run_turns must be between 1 and %d when configured",
			maxScheduleRunTurns,
		)
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

// isLocalBaseURL reports whether the URL points at a loopback, private, or
// link-local address, or a single-label Docker service or machine name, which
// is how unauthenticated local inference and embedding servers appear in
// Compose files and on developer machines. A host with a dot and a host that
// parses as a public IP are reported as public. A URL with no parseable host
// is reported as public too, so requiring a key on a public host fails closed.
func isLocalBaseURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	base, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(base.Hostname())
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	if host == "localhost" {
		return true
	}
	return !strings.Contains(host, ".")
}

// validateOpenAICompatibleBaseURL checks URL syntax. The provider's documented
// API root may include a path (DeepInfra) or be pathless (DeepSeek); startup
// discovery checks whether the configured endpoint actually serves models.
func validateOpenAICompatibleBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%q must be an http or https URL", raw)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
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
