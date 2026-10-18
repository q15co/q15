// Package decision exposes typed decision checks as an agent tool.
package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/decision"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/providertypes"
)

// Check asks a discovered Clef model one choice question.
type Check struct {
	registry *modelcatalog.Registry
}

// NewCheck constructs decision_check using the current provider roster.
func NewCheck(registry *modelcatalog.Registry) *Check {
	return &Check{registry: registry}
}

// Definition returns the fixed decision_check argument schema.
func (t *Check) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "decision_check",
		Description: "Ask a decision model to choose among ordered options using the supplied state. Returns the choice, per-option probabilities, and confidence.",
		PromptGuidance: []string{
			"Confidence measures how concentrated the probabilities are, not whether the choice is correct.",
			"Put the preferred option first to break ties. Include a fallback option when the choices may not cover the state.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{
					"description": "Text or JSON context for the decision.",
					"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "object"},
						map[string]any{"type": "array"},
					},
				},
				"question": map[string]any{"type": "string", "minLength": 1},
				"options": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "minLength": 1},
					"minItems":    2,
					"maxItems":    26,
					"uniqueItems": true,
				},
			},
			"required":             []string{"state", "question", "options"},
			"additionalProperties": false,
		},
	}
}

// Run evaluates one choice without changing the interactive or cognition model selection.
func (t *Check) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		State    json.RawMessage `json:"state"`
		Question string          `json:"question"`
		Options  []string        `json:"options"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("decision_check: invalid arguments JSON")
	}
	if strings.TrimSpace(args.Question) == "" {
		return "", fmt.Errorf("decision_check: question is required")
	}
	model, err := t.model()
	if err != nil {
		return "", err
	}
	base := model.ProviderBaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	client, err := decision.NewClient(base, model.ProviderAPIKey, nil)
	if err != nil {
		return "", err
	}
	options := make([]decision.Option, len(args.Options))
	for i, name := range args.Options {
		options[i] = decision.Option{Name: name}
	}
	result, err := client.Check(ctx, decision.Request{
		Model:     model.ProviderModel,
		State:     args.State,
		Questions: map[string]decision.Question{"check": decision.Choice(args.Question, options)},
	})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result.Answers["check"])
	if err != nil {
		return "", fmt.Errorf("decision_check: encode answer: %w", err)
	}
	return string(data), nil
}

func (t *Check) model() (modelcatalog.Model, error) {
	if t == nil || t.registry == nil {
		return modelcatalog.Model{}, fmt.Errorf("decision_check: model registry is not configured")
	}
	var selected modelcatalog.Model
	best := 3
	for _, model := range t.registry.Snapshot() {
		if model.ProviderType != providertypes.Ollama {
			continue
		}
		rank := 3
		switch strings.SplitN(model.ProviderModel, ":", 2)[0] {
		case "clef":
			rank = 0
		case "clef-flash":
			rank = 1
		}
		if rank < best {
			selected = model
			best = rank
		}
	}
	if best == 3 {
		return modelcatalog.Model{}, fmt.Errorf(
			"decision_check: no Clef decision model in the live Ollama roster; check list_models and provider discovery filters",
		)
	}
	return selected, nil
}
