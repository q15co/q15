package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

// SkillValidator validates one skill directory and returns structured results.
type SkillValidator interface {
	ValidateSkill(path string) (q15skills.ValidationResult, error)
}

// ValidateSkill validates a q15 skill directory.
type ValidateSkill struct {
	validator SkillValidator
}

// NewValidateSkill constructs a validate_skill tool.
func NewValidateSkill(validator SkillValidator) *ValidateSkill {
	return &ValidateSkill{validator: validator}
}

// Definition returns the tool schema exposed to the model.
func (v *ValidateSkill) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "validate_skill",
		Description: "Validate a q15 skill directory and return parsed metadata, errors, and warnings without modifying files",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Skill directory path such as /skills/my-skill, /skills/@builtin/skill-creator, or a workspace-relative draft directory",
				},
			},
			"required": []string{"path"},
		},
	}
}

// Run executes one validate_skill tool call from raw JSON arguments.
func (v *ValidateSkill) Run(_ context.Context, arguments string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	if v.validator == nil {
		return "", fmt.Errorf("no skill validator configured")
	}

	result, err := v.validator.ValidateSkill(args.Path)
	if err != nil {
		return "", err
	}

	lines := []string{
		fmt.Sprintf("Valid: %t", result.Valid),
		"Path: " + args.Path,
	}
	if result.Source != "" {
		lines = append(lines, "Source: "+string(result.Source))
	}
	if result.Name != "" {
		lines = append(lines, "Name: "+result.Name)
	}
	if result.Description != "" {
		lines = append(lines, "Description: "+result.Description)
	}
	if result.SkillPath != "" {
		lines = append(lines, "Skill-Path: "+result.SkillPath)
	}
	if result.SkillFilePath != "" {
		lines = append(lines, "Skill-File: "+result.SkillFilePath)
	}
	if len(result.Warnings) > 0 {
		lines = append(lines, "Warnings:")
		for _, warning := range result.Warnings {
			lines = append(lines, "- "+warning)
		}
	}
	if len(result.Errors) > 0 {
		lines = append(lines, "Errors:")
		for _, validationErr := range result.Errors {
			lines = append(lines, "- "+validationErr)
		}
	}
	return strings.Join(lines, "\n"), nil
}
