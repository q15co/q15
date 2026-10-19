// Package decision exposes typed decision checks as an agent tool.
package decision

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/decision"
)

// Check asks the configured decision client one choice question.
type Check struct {
	client decision.Client
}

// NewCheck constructs decision_check with the application's decision client.
func NewCheck(client decision.Client) *Check {
	return &Check{client: client}
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
	if t == nil || t.client == nil {
		return "", fmt.Errorf("decision_check: decision client is not configured")
	}
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
	options := make([]decision.Option, len(args.Options))
	for i, name := range args.Options {
		options[i] = decision.Option{Name: name}
	}
	request := decision.Request{
		State:     args.State,
		Questions: map[string]decision.Question{"check": decision.Choice(args.Question, options)},
	}
	if err := request.Validate(); err != nil {
		return "", fmt.Errorf("decision_check: %w", err)
	}
	result, err := t.client.Check(ctx, request)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result.Answers["check"])
	if err != nil {
		return "", fmt.Errorf("decision_check: encode answer: %w", err)
	}
	return string(data), nil
}
