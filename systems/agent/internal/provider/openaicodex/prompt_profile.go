package openaicodex

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func appendPromptProfileInstructions(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	profile := strings.TrimSpace(renderPromptProfile())
	if profile == "" {
		return instructions
	}
	if instructions == "" {
		return profile
	}
	return instructions + "\n\n" + profile
}

func renderPromptProfile() string {
	lines := []string{
		"Brief commentary is allowed when it materially helps the user follow multi-step work, but keep it short.",
		"If a tool call is the right next action, prefer the tool call over a long preamble.",
		"Keep commentary tightly coupled to the actual action you are taking.",
	}
	return renderProviderProfile("openai-codex", lines)
}

func renderProviderProfile(provider string, lines []string) string {
	lines = promptBullets(lines)
	if len(lines) == 0 {
		return ""
	}

	attrs := map[string]string{
		"provider": strings.TrimSpace(provider),
	}
	return agent.RenderPromptElement("provider_profile", attrs, strings.Join(lines, "\n"))
}

func promptBullets(lines []string) []string {
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, "- "+line)
	}
	return out
}
