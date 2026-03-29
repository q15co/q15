package openaicompatible

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func withPromptProfile(messages []conversation.Message) []conversation.Message {
	profile := strings.TrimSpace(renderPromptProfile())
	if profile == "" {
		return messages
	}

	out := append([]conversation.Message(nil), messages...)
	out = append(out, conversation.SystemMessage(profile))
	return out
}

func renderPromptProfile() string {
	lines := []string{
		"This endpoint does not preserve assistant commentary disposition metadata in q15, so keep preambles rare and short.",
		"For action requests, make the first substantive reply either a relevant tool call or one concise blocker question when a tool is required.",
		"Do not split the turn into plan-only narration followed by delayed execution.",
	}
	lines = promptBullets(lines)
	if len(lines) == 0 {
		return ""
	}

	attrs := map[string]string{
		"provider": "openai-compatible",
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
