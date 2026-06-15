package ollama

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

	out := conversation.CloneMessages(messages)
	insertAt := leadingSystemMessageCount(out)
	out = append(out, conversation.Message{})
	copy(out[insertAt+1:], out[insertAt:])
	out[insertAt] = conversation.SystemMessage(profile)
	return out
}

func leadingSystemMessageCount(messages []conversation.Message) int {
	count := 0
	for _, message := range messages {
		if message.Role != conversation.SystemRole {
			break
		}
		count++
	}
	return count
}

func renderPromptProfile() string {
	lines := []string{
		"Ollama responses are replayed through q15's portable transcript model, so keep ordinary answer text in content and put tool requests only in tool_calls.",
		"For action requests, make the first substantive reply either a relevant tool call or one concise blocker question when a tool is required.",
		"Do not include raw thinking tags in final answer text.",
	}
	lines = promptBullets(lines)
	if len(lines) == 0 {
		return ""
	}

	attrs := map[string]string{
		"provider": "ollama",
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
