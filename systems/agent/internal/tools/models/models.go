// Package models exposes model-roster and runtime model-selection tools.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
)

const maxListModelsLimit = 500

// ListProviders reports configured providers and whether each currently has a
// non-empty live roster.
type ListProviders struct {
	registry  *modelcatalog.Registry
	selection *modelcatalog.Selection
}

// ListModels reports the live roster with discovery/enrichment metadata.
type ListModels struct {
	registry  *modelcatalog.Registry
	selection *modelcatalog.Selection
}

// SwitchModel mutates the session-scoped current interactive model and
// persists the change so it survives restart.
type SwitchModel struct {
	registry  *modelcatalog.Registry
	selection *modelcatalog.Selection
	store     *selectionstore.Store
}

// SwitchCognitionModel mutates and persists the model used by one background
// cognition job type.
type SwitchCognitionModel struct {
	registry *modelcatalog.Registry
	store    *selectionstore.Store
	jobTypes map[string]struct{}
	jobOrder []string
}

type providerInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url,omitempty"`
	Reachable bool   `json:"reachable"`
	Current   bool   `json:"current,omitempty"`
}

type modelInfo struct {
	Provider         string             `json:"provider"`
	ProviderType     string             `json:"provider_type"`
	ProviderModel    string             `json:"provider_model"`
	Ref              string             `json:"ref"`
	Name             string             `json:"name,omitempty"`
	Current          bool               `json:"current,omitempty"`
	Modalities       []string           `json:"modalities"`
	Capabilities     []string           `json:"capabilities"`
	StructuredOutput bool               `json:"structured_output"`
	Params           int64              `json:"params,omitempty"`
	Context          int                `json:"context,omitempty"`
	Output           int                `json:"output,omitempty"`
	ReleaseDate      string             `json:"release_date,omitempty"`
	CostTier         string             `json:"cost_tier,omitempty"`
	CostPerMTokIn    *float64           `json:"cost_per_mtok_in,omitempty"`
	CostPerMTokOut   *float64           `json:"cost_per_mtok_out,omitempty"`
	Benchmarks       map[string]float64 `json:"benchmarks,omitempty"`
	Source           string             `json:"source,omitempty"`
}

// NewListProviders constructs the list_providers tool.
func NewListProviders(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
) *ListProviders {
	return &ListProviders{registry: registry, selection: selection}
}

// NewListModels constructs the list_models tool.
func NewListModels(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
) *ListModels {
	return &ListModels{registry: registry, selection: selection}
}

// NewSwitchModel constructs the switch_model tool.
func NewSwitchModel(
	registry *modelcatalog.Registry,
	selection *modelcatalog.Selection,
	store *selectionstore.Store,
) *SwitchModel {
	return &SwitchModel{registry: registry, selection: selection, store: store}
}

// NewSwitchCognitionModel constructs the switch_cognition_model tool. jobTypes
// is the set of registered cognition job type identifiers the tool may target.
func NewSwitchCognitionModel(
	registry *modelcatalog.Registry,
	store *selectionstore.Store,
	jobTypes []string,
) *SwitchCognitionModel {
	tool := &SwitchCognitionModel{registry: registry, store: store}
	for _, jobType := range jobTypes {
		jobType = strings.TrimSpace(jobType)
		if jobType == "" {
			continue
		}
		if tool.jobTypes == nil {
			tool.jobTypes = map[string]struct{}{}
		}
		if _, ok := tool.jobTypes[jobType]; ok {
			continue
		}
		tool.jobTypes[jobType] = struct{}{}
		tool.jobOrder = append(tool.jobOrder, jobType)
	}
	return tool
}

// Definition returns the list_providers schema exposed to the model.
func (t *ListProviders) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "list_providers",
		Description: "List configured model providers and whether each currently has reachable live models.",
		PromptGuidance: []string{
			"Use before switching providers when you need to know which provider rosters are currently reachable.",
			"A provider is reachable when it has at least one model in the current live roster.",
		},
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// Run executes list_providers.
func (t *ListProviders) Run(_ context.Context, _ string) (string, error) {
	if t == nil || t.registry == nil {
		return "", fmt.Errorf("model registry is not configured")
	}
	currentProvider := ""
	if t.selection != nil {
		currentProvider = t.selection.CurrentProvider()
	}

	providers := t.registry.Providers()
	out := make([]providerInfo, 0, len(providers))
	for _, provider := range providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			continue
		}
		out = append(out, providerInfo{
			Name:      name,
			Type:      strings.TrimSpace(provider.Type),
			BaseURL:   strings.TrimSpace(provider.BaseURL),
			Reachable: t.registry.ProviderHasModels(name),
			Current:   name == currentProvider,
		})
	}

	return jsonOutput(map[string]any{
		"current_provider": currentProvider,
		"providers":        out,
	})
}

// Definition returns the list_models schema exposed to the model.
func (t *ListModels) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "list_models",
		Description: "List live models with discovery metadata. Supports provider, capability, min_context, and limit filters.",
		PromptGuidance: []string{
			"Use when deciding whether the current model is appropriate for a task, or before switch_model.",
			"Filter by provider, capability, min_context, or limit when the roster is large.",
			"Do not switch just because alternatives exist; switch when you have a specific capability, context, freshness, or cost reason.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional provider name filter.",
				},
				"capability": map[string]any{
					"type":        "string",
					"description": "Optional capability filter: text, tool_calling/tools, reasoning, image_input/vision, audio_input/audio, video_input/video, structured_output/json.",
				},
				"min_context": map[string]any{
					"type":        "integer",
					"description": "Optional minimum context window in tokens. Models with unknown context are excluded when this is set.",
					"minimum":     0,
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Optional maximum models to return, capped at 500. Omit or set 0 for all matches.",
					"minimum":     0,
					"maximum":     maxListModelsLimit,
				},
			},
		},
	}
}

// Run executes list_models.
func (t *ListModels) Run(_ context.Context, arguments string) (string, error) {
	if t == nil || t.registry == nil {
		return "", fmt.Errorf("model registry is not configured")
	}
	var args struct {
		Provider   string `json:"provider"`
		Capability string `json:"capability"`
		MinContext int    `json:"min_context"`
		Limit      int    `json:"limit"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments JSON: %w", err)
		}
	}
	providerFilter := strings.TrimSpace(args.Provider)
	capabilityFilter := normalizeCapability(args.Capability)
	if strings.TrimSpace(args.Capability) != "" && capabilityFilter == "" {
		return "", fmt.Errorf("unsupported capability %q", args.Capability)
	}
	if args.MinContext < 0 {
		return "", fmt.Errorf("min_context must be >= 0")
	}
	if args.Limit < 0 {
		return "", fmt.Errorf("limit must be >= 0")
	}
	if args.Limit > maxListModelsLimit {
		return "", fmt.Errorf("limit must be <= %d", maxListModelsLimit)
	}

	currentProvider, currentModel := "", ""
	if t.selection != nil {
		currentProvider, currentModel = t.selection.Current()
	}

	matches := make([]modelInfo, 0)
	matchedBeforeLimit := 0
	for _, model := range t.registry.Snapshot() {
		if providerFilter != "" && model.ProviderName != providerFilter {
			continue
		}
		if capabilityFilter != "" && !modelHasCapability(model, capabilityFilter) {
			continue
		}
		if args.MinContext > 0 && model.MaxContextTokens < args.MinContext {
			continue
		}
		matchedBeforeLimit++
		if args.Limit > 0 && len(matches) >= args.Limit {
			continue
		}
		matches = append(matches, describeModel(
			model,
			model.ProviderName == currentProvider && model.Ref == currentModel,
		))
	}

	return jsonOutput(map[string]any{
		"count":     len(matches),
		"total":     matchedBeforeLimit,
		"truncated": args.Limit > 0 && matchedBeforeLimit > len(matches),
		"models":    matches,
	})
}

// Definition returns the switch_model schema exposed to the model.
func (t *SwitchModel) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "switch_model",
		Description: "Switch the session-scoped current provider and model. The pair must exist in the live roster.",
		PromptGuidance: []string{
			"Use only after you have a specific reason to switch, such as required capability, larger context, fresher model, or cost.",
			"Call list_providers/list_models first when you need to discover valid provider and model values.",
			"A switch takes effect on the next model turn.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"provider": map[string]any{
					"type":        "string",
					"description": "Configured provider name hosting the model.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Agent-side model ref from list_models (not the provider_model tag).",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional concise reason for the switch; logged for auditability.",
				},
			},
			"required": []string{"provider", "model"},
		},
	}
}

// Run executes switch_model.
func (t *SwitchModel) Run(_ context.Context, arguments string) (string, error) {
	if t == nil || t.registry == nil || t.selection == nil {
		return "", fmt.Errorf("model selection is not configured")
	}
	var args struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	provider := strings.TrimSpace(args.Provider)
	modelRef := strings.TrimSpace(args.Model)
	model, ok := t.registry.Lookup(provider, modelRef)
	if !ok {
		return "", fmt.Errorf(
			"model %q for provider %q is not in the current live roster",
			modelRef,
			provider,
		)
	}
	// Persist before mutating the live selection so a persistence failure leaves
	// the running agent unchanged rather than switching without durability.
	if t.store != nil {
		if err := t.store.SetInteractive(provider, modelRef); err != nil {
			return "", fmt.Errorf("persist model selection: %w", err)
		}
	}
	if err := t.selection.Set(provider, modelRef); err != nil {
		return "", err
	}
	reason := strings.TrimSpace(args.Reason)
	log.Printf("q15: switch_model provider=%q model=%q reason=%q", provider, modelRef, reason)

	return jsonOutput(map[string]any{
		"current": describeModel(model, true),
		"reason":  reason,
	})
}

// Definition returns the switch_cognition_model schema exposed to the model.
func (t *SwitchCognitionModel) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "switch_cognition_model",
		Description: "Set or clear the model used by one background cognition job type. Jobs without an override inherit the interactive model.",
		PromptGuidance: []string{
			"Use to run a specific background cognition job on a different model than the interactive one (for example a cheaper model for consolidation, or a stronger model for extraction).",
			"Valid job_type values are the registered cognition jobs; pass action \"clear\" to restore inheritance from the interactive model.",
			"The pair must exist in the live roster, and the change takes effect on the next run of that job.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_type": map[string]any{
					"type":        "string",
					"description": "Registered cognition job type identifier.",
					"enum":        append([]string(nil), t.jobOrder...),
				},
				"action": map[string]any{
					"type":        "string",
					"description": "Optional action: omit (default) to set the model, or \"clear\" to remove the override.",
					"enum":        []string{"set", "clear"},
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "Configured provider name hosting the model (required for action \"set\").",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Agent-side model ref from list_models (required for action \"set\").",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional concise reason for the switch; logged for auditability.",
				},
			},
			"required": []string{"job_type"},
		},
	}
}

// Run executes switch_cognition_model.
func (t *SwitchCognitionModel) Run(_ context.Context, arguments string) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("cognition selection store is not configured")
	}
	var args struct {
		JobType  string `json:"job_type"`
		Action   string `json:"action"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	jobType := strings.TrimSpace(args.JobType)
	if jobType == "" {
		return "", fmt.Errorf("job_type is required")
	}
	if len(t.jobTypes) > 0 {
		if _, ok := t.jobTypes[jobType]; !ok {
			return "", fmt.Errorf("job_type %q is not a registered cognition job", jobType)
		}
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "set"
	}
	if action != "set" && action != "clear" {
		return "", fmt.Errorf("action must be \"set\" or \"clear\", got %q", args.Action)
	}
	reason := strings.TrimSpace(args.Reason)

	if action == "clear" {
		if err := t.store.ClearCognition(jobType); err != nil {
			return "", fmt.Errorf("clear cognition selection: %w", err)
		}
		log.Printf("q15: switch_cognition_model job=%q action=clear reason=%q", jobType, reason)
		return jsonOutput(map[string]any{
			"job_type": jobType,
			"action":   "clear",
			"reason":   reason,
		})
	}

	provider := strings.TrimSpace(args.Provider)
	modelRef := strings.TrimSpace(args.Model)
	if t.registry == nil {
		return "", fmt.Errorf("model registry is not configured")
	}
	model, ok := t.registry.Lookup(provider, modelRef)
	if !ok {
		return "", fmt.Errorf(
			"model %q for provider %q is not in the current live roster",
			modelRef,
			provider,
		)
	}
	if err := t.store.SetCognition(jobType, provider, modelRef); err != nil {
		return "", fmt.Errorf("persist cognition selection: %w", err)
	}
	log.Printf(
		"q15: switch_cognition_model job=%q provider=%q model=%q reason=%q",
		jobType,
		provider,
		modelRef,
		reason,
	)
	return jsonOutput(map[string]any{
		"job_type": jobType,
		"action":   "set",
		"current":  describeModel(model, true),
		"reason":   reason,
	})
}

func describeModel(m modelcatalog.Model, current bool) modelInfo {
	info := modelInfo{
		Provider:         m.ProviderName,
		ProviderType:     m.ProviderType,
		ProviderModel:    m.ProviderModel,
		Ref:              m.Ref,
		Name:             m.Name,
		Current:          current,
		Modalities:       modelModalities(m),
		Capabilities:     modelCapabilities(m),
		StructuredOutput: m.StructuredOutput,
		Params:           m.ParameterCount,
		Context:          m.MaxContextTokens,
		Output:           m.MaxOutputTokens,
		CostTier:         m.CostTier,
		Benchmarks:       m.BenchmarkScores,
		Source:           m.Source,
	}
	if !m.ReleaseDate.IsZero() {
		info.ReleaseDate = m.ReleaseDate.Format(time.DateOnly)
	}
	if m.CostPerMTokIn > 0 {
		v := m.CostPerMTokIn
		info.CostPerMTokIn = &v
	}
	if m.CostPerMTokOut > 0 {
		v := m.CostPerMTokOut
		info.CostPerMTokOut = &v
	}
	return info
}

func modelModalities(m modelcatalog.Model) []string {
	modalities := make([]string, 0, 4)
	if m.Capabilities.Text {
		modalities = append(modalities, "text")
	}
	if m.Capabilities.ImageInput {
		modalities = append(modalities, "image_input")
	}
	if m.Capabilities.AudioInput {
		modalities = append(modalities, "audio_input")
	}
	if m.VideoInput {
		modalities = append(modalities, "video_input")
	}
	return modalities
}

func modelCapabilities(m modelcatalog.Model) []string {
	capabilities := make([]string, 0, 3)
	if m.Capabilities.ToolCalling {
		capabilities = append(capabilities, "tool_calling")
	}
	if m.Capabilities.Reasoning {
		capabilities = append(capabilities, "reasoning")
	}
	if m.StructuredOutput {
		capabilities = append(capabilities, "structured_output")
	}
	return capabilities
}

func normalizeCapability(capability string) string {
	switch strings.ToLower(strings.TrimSpace(capability)) {
	case "":
		return ""
	case "text":
		return "text"
	case "tool", "tools", "tool_calling":
		return "tool_calling"
	case "reasoning":
		return "reasoning"
	case "image", "image_input", "vision":
		return "image_input"
	case "audio", "audio_input":
		return "audio_input"
	case "video", "video_input":
		return "video_input"
	case "structured", "structured_output", "json":
		return "structured_output"
	default:
		return ""
	}
}

func modelHasCapability(m modelcatalog.Model, capability string) bool {
	switch capability {
	case "text":
		return m.Capabilities.Text
	case "tool_calling":
		return m.Capabilities.ToolCalling
	case "reasoning":
		return m.Capabilities.Reasoning
	case "image_input":
		return m.Capabilities.ImageInput
	case "audio_input":
		return m.Capabilities.AudioInput
	case "video_input":
		return m.VideoInput
	case "structured_output":
		return m.StructuredOutput
	default:
		return false
	}
}

func jsonOutput(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
