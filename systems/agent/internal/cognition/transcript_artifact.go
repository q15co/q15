package cognition

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func renderTranscriptArtifact(messages []conversation.Message) string {
	if len(messages) == 0 {
		return "No transcript messages were loaded."
	}

	renderedMessages := make([]string, 0, len(messages))
	for i, msg := range messages {
		body := renderTranscriptMessage(msg)
		if strings.TrimSpace(body) == "" {
			body = "(message had no renderable parts)"
		}
		rendered := agent.RenderPromptElement("message", map[string]string{
			"index": strconv.Itoa(i + 1),
			"role":  string(msg.Role),
		}, body)
		if rendered == "" {
			continue
		}
		renderedMessages = append(renderedMessages, rendered)
	}
	if len(renderedMessages) == 0 {
		return "No transcript messages were loaded."
	}
	return strings.Join(renderedMessages, "\n\n")
}

func renderTranscriptScope(capTurns int, loadedMessages int) string {
	if loadedMessages == 0 {
		return "No transcript messages were loaded for this run."
	}
	return renderPromptLines(
		fmt.Sprintf(
			"A bounded replay slice of episodic history, selected by checkpoint-aware replay policy and capped at %d turns, is included below as a transcript artifact.",
			capTurns,
		),
		"Treat it as historical evidence for unconsolidated or still-relevant context, not as the full transcript.",
	)
}

func renderTranscriptMessage(msg conversation.Message) string {
	renderedParts := make([]string, 0, len(msg.Parts)+1)
	if msg.Role == conversation.UserRole {
		if tag := conversation.RenderUserMessageMetadataTag(msg); strings.TrimSpace(tag) != "" {
			renderedParts = append(renderedParts, tag)
		}
	}
	for i, part := range msg.Parts {
		body, attrs := renderTranscriptPart(part)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs["index"] = strconv.Itoa(i + 1)
		attrs["type"] = string(part.Type)
		rendered := agent.RenderPromptElement("part", attrs, body)
		if rendered == "" {
			continue
		}
		renderedParts = append(renderedParts, rendered)
	}
	return strings.Join(renderedParts, "\n\n")
}

func renderTranscriptPart(part conversation.Part) (string, map[string]string) {
	switch part.Type {
	case conversation.TextPartType:
		body := strings.TrimSpace(part.Text)
		if body == "" {
			return "", nil
		}
		attrs := map[string]string{}
		if disposition := strings.TrimSpace(string(part.Disposition)); disposition != "" {
			attrs["disposition"] = disposition
		}
		return body, attrs
	case conversation.ReasoningPartType:
		body := strings.TrimSpace(part.Text)
		if body == "" {
			body = conversation.PortableReasoningUnavailableText
		}
		return body, nil
	case conversation.ImagePartType:
		lines := []string{"Image attachment omitted from transcript artifact."}
		if mediaRef := strings.TrimSpace(part.MediaRef); mediaRef != "" {
			lines = append(lines, "Media-Ref: "+mediaRef)
		}
		if dataURL := strings.TrimSpace(part.DataURL); dataURL != "" {
			lines = append(lines, "Data-URL: present")
		}
		return strings.Join(lines, "\n"), nil
	case conversation.ToolCallPartType:
		body := strings.TrimSpace(part.Arguments)
		if body == "" {
			body = "{}"
		}
		attrs := map[string]string{}
		if id := strings.TrimSpace(part.ID); id != "" {
			attrs["id"] = id
		}
		if name := strings.TrimSpace(part.Name); name != "" {
			attrs["name"] = name
		}
		return body, attrs
	case conversation.ToolResultPartType:
		body := strings.TrimSpace(part.Content)
		if body == "" {
			body = "(empty tool result)"
		}
		attrs := map[string]string{}
		if toolCallID := strings.TrimSpace(part.ToolCallID); toolCallID != "" {
			attrs["tool_call_id"] = toolCallID
		}
		if part.IsError {
			attrs["is_error"] = "true"
		}
		return body, attrs
	default:
		return "", nil
	}
}
