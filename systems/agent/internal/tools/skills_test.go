package tools

import (
	"context"
	"strings"
	"testing"

	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

type stubSkillValidator struct {
	path string
}

func (s *stubSkillValidator) ValidateSkill(path string) (q15skills.ValidationResult, error) {
	s.path = path
	return q15skills.ValidationResult{
		Valid:         false,
		Name:          "demo-skill",
		Description:   "Demo description",
		Source:        q15skills.SourceDraft,
		SkillPath:     "/workspace/demo-skill",
		SkillFilePath: "/workspace/demo-skill/SKILL.md",
		Warnings:      []string{"SKILL.md exceeds 500 lines; prefer progressive disclosure"},
		Errors:        []string{"frontmatter field \"description\" is required"},
	}, nil
}

func TestValidateSkillDefinitionAndRun(t *testing.T) {
	t.Parallel()

	validator := &stubSkillValidator{}
	tool := NewValidateSkill(validator)
	if got, want := tool.Definition().Name, "validate_skill"; got != want {
		t.Fatalf("Definition().Name = %q, want %q", got, want)
	}

	got, err := tool.Run(context.Background(), `{"path":"/workspace/demo-skill"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if validator.path != "/workspace/demo-skill" {
		t.Fatalf("validator.path = %q", validator.path)
	}
	for _, want := range []string{
		"Valid: false",
		"Path: /workspace/demo-skill",
		"Source: draft",
		"Name: demo-skill",
		"Description: Demo description",
		"Warnings:",
		"Errors:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q:\n%s", want, got)
		}
	}
}

func TestValidateSkillRunErrorsOnMissingPath(t *testing.T) {
	t.Parallel()

	tool := NewValidateSkill(&stubSkillValidator{})
	if _, err := tool.Run(context.Background(), `{"path":"   "}`); err == nil ||
		!strings.Contains(err.Error(), "missing required argument: path") {
		t.Fatalf("Run() error = %v, want missing path error", err)
	}
}
