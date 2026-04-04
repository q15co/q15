package cognition

import (
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestRenderPromptUsesDistinctCognitionContract(t *testing.T) {
	prompt, err := renderPrompt("working_memory.consolidate", Spec{
		Objective:          "Compress recent internal state into a bounded working-memory update.",
		CompletionContract: "Return a single `status: <value>` line.",
		PromptSections: []agent.PromptSection{
			{
				Name: "job_context",
				Body: "- recent turns are provided separately",
			},
		},
	})
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}

	for _, want := range []string{
		"<cognition_mode>",
		"<job_objective type=\"working_memory.consolidate\">",
		"<evidence_rules>",
		"<non_user_facing_behavior>",
		"<completion_contract>",
		"<job_context>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	for _, unwanted := range []string{
		"<identity>",
		"Resolve the whole user request",
		"Final answers should be concise",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt should not contain %q:\n%s", unwanted, prompt)
		}
	}
}
