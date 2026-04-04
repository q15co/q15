package cognition

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// Spec describes one typed cognition job execution.
type Spec struct {
	Objective          string
	CompletionContract string
	InputMessages      []conversation.Message
	PromptSections     []agent.PromptSection
	ExposeTools        bool
}

func renderPrompt(jobType string, spec Spec) (string, error) {
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return "", fmt.Errorf("job type is required")
	}

	objective := strings.TrimSpace(spec.Objective)
	if objective == "" {
		return "", fmt.Errorf("job objective is required")
	}

	completionContract := strings.TrimSpace(spec.CompletionContract)
	if completionContract == "" {
		return "", fmt.Errorf("completion contract is required")
	}

	sections := []agent.PromptSection{
		{
			Name: "cognition_mode",
			Body: renderPromptLines(
				"- You are running a background cognition/control job.",
				"- You are maintaining internal agent state, not replying to a user.",
			),
		},
		{
			Name:       "job_objective",
			Attributes: map[string]string{"type": jobType},
			Body:       objective,
		},
		{
			Name: "evidence_rules",
			Body: renderPromptLines(
				"- Do not invent facts.",
				"- Reflect only what is supported by the provided context, transcript evidence, tool outputs, or durable memory.",
				"- Preserve unresolved items explicitly instead of silently resolving uncertainty.",
			),
		},
		{
			Name: "non_user_facing_behavior",
			Body: renderPromptLines(
				"- Produce compact, deterministic internal output rather than conversational prose.",
				"- Do not address a user, narrate user-facing progress, or add pleasantries.",
				"- Keep the update bounded, current, and auditable.",
			),
		},
		{
			Name: "completion_contract",
			Body: completionContract,
		},
	}
	sections = append(sections, spec.PromptSections...)
	return agent.ComposePromptSections(sections...), nil
}

func renderPromptLines(lines ...string) string {
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}
