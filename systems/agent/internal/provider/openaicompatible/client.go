// Package openaicompatible implements an OpenAI Chat Completions-compatible
// model client.
package openaicompatible

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

// Client adapts an OpenAI-compatible Chat Completions API to the agent model
// interface.
type Client struct {
	client     openai.Client
	mediaStore q15media.Store
}

var _ agent.ModelClient = (*Client)(nil)

const openAICompatibleReplayKey = "openai_compatible"

const synthesizedReasoningContent = "Portable reasoning summary unavailable for replay; continuing with prior reasoning state."

const toolImageFollowupText = "Use the attached image output from the previous tool result when continuing."

const systemOnlyFollowupText = "Use the system instructions above as the full task definition and complete them directly."

// NewClient constructs a Chat Completions-compatible model client.
func NewClient(baseURL string, apiKey string, mediaStore q15media.Store) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("provider base url is required")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("provider api key is required")
	}

	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	)

	return &Client{
		client:     client,
		mediaStore: mediaStore,
	}, nil
}

// Complete sends one completion request to the configured compatible endpoint.
func (c *Client) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	if strings.TrimSpace(model) == "" {
		return agent.ModelClientResult{}, fmt.Errorf("model name is required")
	}

	reqMessages, err := mapMessages(withPromptProfile(messages), c.mediaStore)
	if err != nil {
		return agent.ModelClientResult{}, err
	}
	reqTools := mapTools(tools)

	chatCompletion, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Messages: reqMessages,
		Model:    model,
		Tools:    reqTools,
	})
	if err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("chat completion: %w", err)
	}
	if len(chatCompletion.Choices) == 0 {
		return agent.ModelClientResult{}, fmt.Errorf("chat completion returned no choices")
	}

	choice := chatCompletion.Choices[0]
	assistantMessage, err := parseAssistantMessage(
		json.RawMessage(choice.Message.RawJSON()),
		choice.Message.ToolCalls,
	)
	if err != nil {
		return agent.ModelClientResult{}, err
	}

	return agent.ModelClientResult{
		Messages:     assistantMessage,
		FinishReason: choice.FinishReason,
	}, nil
}

func mapMessages(
	messages []conversation.Message,
	mediaStore q15media.Store,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+1)
	droppedReplayParts := 0
	hasUserMessage := false
	hasSystemMessage := false
	for _, message := range messages {
		switch message.Role {
		case conversation.SystemRole:
			hasSystemMessage = true
		case conversation.UserRole:
			hasUserMessage = true
		}
	}
	needsBootstrap := hasSystemMessage && !hasUserMessage
	insertedBootstrap := false

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if needsBootstrap && !insertedBootstrap && msg.Role != conversation.SystemRole {
			out = append(out, openai.UserMessage(systemOnlyFollowupText))
			insertedBootstrap = true
		}
		switch msg.Role {
		case conversation.SystemRole:
			text, err := textOnlyMessageContent(msg)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, openai.SystemMessage(trimmed))
			}
		case conversation.UserRole:
			userMessage, err := buildUserMessage(msg, mediaStore)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			out = append(out, userMessage)
		case conversation.AssistantRole:
			group := []conversation.Message{msg}
			for i+1 < len(messages) && messages[i+1].Role == conversation.AssistantRole {
				i++
				group = append(group, messages[i])
			}
			raw, ok, dropped, err := buildAssistantReplayMessage(group)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			droppedReplayParts += dropped
			if !ok {
				continue
			}
			out = append(
				out,
				param.Override[openai.ChatCompletionMessageParamUnion](raw),
			)
		case conversation.ToolRole:
			toolParts := 0
			imageParts := make([]conversation.Part, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				part = conversation.NormalizePart(part)
				switch part.Type {
				case conversation.ToolResultPartType:
					toolParts++
					if strings.TrimSpace(part.ToolCallID) == "" {
						return nil, fmt.Errorf("message %d: tool result missing tool call id", i)
					}
					out = append(
						out,
						openai.ToolMessage(part.Content, part.ToolCallID),
					)
				case conversation.ImagePartType:
					imageParts = append(imageParts, part)
				}
			}
			if toolParts == 0 && len(imageParts) == 0 {
				return nil, fmt.Errorf("message %d: tool message missing tool result part", i)
			}
			if len(imageParts) > 0 {
				followup, err := buildToolImageFollowupMessage(imageParts, mediaStore)
				if err != nil {
					return nil, fmt.Errorf("message %d: %w", i, err)
				}
				out = append(out, followup)
			}
		default:
			return nil, fmt.Errorf("message %d: unsupported role %q", i, msg.Role)
		}
	}
	if needsBootstrap && !insertedBootstrap {
		out = append(out, openai.UserMessage(systemOnlyFollowupText))
	}

	if droppedReplayParts > 0 {
		log.Printf(
			"q15: openai-compatible request dropped unmatched reasoning replay state parts=%d and continued without opaque replay",
			droppedReplayParts,
		)
	}

	return out, nil
}

func buildUserMessage(
	msg conversation.Message,
	mediaStore q15media.Store,
) (openai.ChatCompletionMessageParamUnion, error) {
	msg = conversation.PromptVisibleUserMessage(msg)
	textOnly := true
	contentParts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(msg.Parts))
	var textBuilder strings.Builder

	for _, part := range conversation.NormalizeParts(msg.Parts) {
		switch part.Type {
		case conversation.TextPartType:
			textBuilder.WriteString(part.Text)
			contentParts = append(contentParts, openai.TextContentPart(part.Text))
		case conversation.ImagePartType:
			textOnly = false
			dataURL, err := q15media.ResolveImagePartDataURL(
				part,
				mediaStore,
				q15media.DefaultMaxImageBytes,
			)
			if err != nil {
				return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf(
					"resolve image input: %w",
					err,
				)
			}
			contentParts = append(contentParts, openai.ImageContentPart(
				openai.ChatCompletionContentPartImageImageURLParam{URL: dataURL},
			))
		default:
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf(
				"unsupported user message part type %q",
				part.Type,
			)
		}
	}

	if textOnly {
		return openai.UserMessage(textBuilder.String()), nil
	}
	return openai.UserMessage(contentParts), nil
}

func buildToolImageFollowupMessage(
	imageParts []conversation.Part,
	mediaStore q15media.Store,
) (openai.ChatCompletionMessageParamUnion, error) {
	parts := make([]conversation.Part, 0, 1+len(imageParts))
	parts = append(parts, conversation.Text(toolImageFollowupText, ""))
	parts = append(parts, conversation.CloneParts(imageParts)...)
	return buildUserMessage(conversation.UserMessageParts(parts...), mediaStore)
}

func textOnlyMessageContent(msg conversation.Message) (string, error) {
	var builder strings.Builder
	for _, part := range conversation.NormalizeParts(msg.Parts) {
		if part.Type != conversation.TextPartType {
			return "", fmt.Errorf("unsupported non-text part %q in text-only message", part.Type)
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}

func mapTools(tools []agent.ToolDefinition) []openai.ChatCompletionToolUnionParam {
	if len(tools) == 0 {
		return nil
	}

	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}

		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		definition := openai.FunctionDefinitionParam{
			Name:       name,
			Parameters: openai.FunctionParameters(parameters),
		}
		if desc := strings.TrimSpace(tool.Description); desc != "" {
			definition.Description = openai.String(desc)
		}

		out = append(out, openai.ChatCompletionFunctionTool(definition))
	}

	return out
}

func parseAssistantMessage(
	raw json.RawMessage,
	toolCalls []openai.ChatCompletionMessageToolCallUnion,
) ([]conversation.Message, error) {
	var reasoningText string
	var reasoningOpaque string
	if len(raw) > 0 {
		var probe struct {
			Content          string `json:"content"`
			Refusal          string `json:"refusal"`
			ReasoningContent string `json:"reasoning_content"`
			ReasoningOpaque  string `json:"reasoning_opaque"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil {
			reasoningText = strings.TrimSpace(probe.ReasoningContent)
			reasoningOpaque = strings.TrimSpace(probe.ReasoningOpaque)
			if probe.Content == "" && probe.Refusal != "" {
				var fallback struct {
					Content string `json:"content"`
				}
				if content := strings.TrimSpace(probe.Refusal); content != "" {
					fallback.Content = content
				}
				_ = fallback
			}
		}
	}

	content := ""
	refusal := ""
	if len(raw) > 0 {
		var probe struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		}
		if err := json.Unmarshal(raw, &probe); err == nil {
			content = strings.TrimSpace(probe.Content)
			refusal = strings.TrimSpace(probe.Refusal)
		}
	}
	if content == "" {
		content = refusal
	}

	parts := make([]conversation.Part, 0, 2+len(toolCalls))
	if reasoningText != "" || reasoningOpaque != "" {
		var replay map[string]json.RawMessage
		if reasoningOpaque != "" {
			rawReplay, err := json.Marshal(map[string]string{
				"reasoning_opaque": reasoningOpaque,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal reasoning replay: %w", err)
			}
			replay = map[string]json.RawMessage{
				openAICompatibleReplayKey: rawReplay,
			}
		}
		parts = append(parts, conversation.Reasoning(reasoningText, replay))
	}
	if content != "" {
		parts = append(parts, conversation.Text(content, ""))
	}

	for _, toolCall := range toolCalls {
		switch call := toolCall.AsAny().(type) {
		case openai.ChatCompletionMessageFunctionToolCall:
			parts = append(parts, conversation.ToolCall(
				call.ID,
				call.Function.Name,
				call.Function.Arguments,
			))
		default:
			return nil, fmt.Errorf("unsupported tool call type %q", toolCall.Type)
		}
	}

	if len(parts) == 0 {
		return nil, nil
	}
	return []conversation.Message{conversation.AssistantMessage(parts...)}, nil
}

func buildAssistantReplayMessage(
	messages []conversation.Message,
) (json.RawMessage, bool, int, error) {
	text := ""
	reasoningText := ""
	reasoningOpaque := ""
	toolCalls := make([]map[string]any, 0)
	droppedReplayParts := 0

	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch part.Type {
			case conversation.TextPartType:
				text += part.Text
			case conversation.ReasoningPartType:
				if strings.TrimSpace(part.Text) != "" {
					if reasoningText == "" {
						reasoningText = part.Text
					} else {
						reasoningText += "\n" + part.Text
					}
				}
				if opaque := extractCompatibleReasoningOpaque(part.Replay); opaque != "" {
					if reasoningOpaque == "" {
						reasoningOpaque = opaque
					}
				} else if len(part.Replay) > 0 && strings.TrimSpace(part.Text) == "" {
					droppedReplayParts++
				}
			case conversation.ToolCallPartType:
				arguments, err := normalizeToolCallArguments(part.Arguments)
				if err != nil {
					return nil, false, droppedReplayParts, err
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   part.ID,
					"type": "function",
					"function": map[string]any{
						"name":      part.Name,
						"arguments": arguments,
					},
				})
			}
		}
	}

	if len(toolCalls) > 0 && strings.TrimSpace(reasoningText) == "" {
		reasoningText = synthesizedReasoningContent
	}

	if strings.TrimSpace(text) == "" && reasoningText == "" && reasoningOpaque == "" &&
		len(toolCalls) == 0 {
		return nil, false, droppedReplayParts, nil
	}

	payload := map[string]any{"role": "assistant"}
	trimmedText := strings.TrimSpace(text)
	if len(toolCalls) > 0 && trimmedText == "" {
		payload["content"] = nil
	} else {
		payload["content"] = trimmedText
	}
	if reasoningText != "" {
		payload["reasoning_content"] = reasoningText
	}
	if reasoningOpaque != "" {
		payload["reasoning_opaque"] = reasoningOpaque
	}
	if len(toolCalls) > 0 {
		payload["tool_calls"] = toolCalls
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, droppedReplayParts, fmt.Errorf(
			"marshal assistant replay message: %w",
			err,
		)
	}
	return raw, true, droppedReplayParts, nil
}

func normalizeToolCallArguments(arguments string) (string, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		return "", fmt.Errorf("decode tool call arguments: invalid JSON")
	}
	return arguments, nil
}

func extractCompatibleReasoningOpaque(replay map[string]json.RawMessage) string {
	raw := replay[openAICompatibleReplayKey]
	if len(raw) == 0 {
		return ""
	}

	var probe struct {
		ReasoningOpaque string `json:"reasoning_opaque"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.ReasoningOpaque)
}
