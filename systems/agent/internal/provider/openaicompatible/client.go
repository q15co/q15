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
)

// Client adapts an OpenAI-compatible Chat Completions API to the agent model
// interface.
type Client struct {
	client openai.Client
}

var _ agent.ModelClient = (*Client)(nil)

const openAICompatibleReplayKey = "openai_compatible"

const synthesizedReasoningContent = "Portable reasoning summary unavailable for replay; continuing with prior reasoning state."

// NewClient constructs a Chat Completions-compatible model client.
func NewClient(baseURL string, apiKey string) (*Client, error) {
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

	return &Client{client: client}, nil
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

	reqMessages, err := mapMessages(withPromptProfile(messages))
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
) ([]openai.ChatCompletionMessageParamUnion, error) {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	droppedReplayParts := 0

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		switch msg.Role {
		case conversation.SystemRole:
			out = append(out, openai.SystemMessage(conversation.TextValue(msg)))
		case conversation.UserRole:
			out = append(out, openai.UserMessage(conversation.TextValue(msg)))
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
			out = append(out, param.Override[openai.ChatCompletionMessageParamUnion](raw))
		case conversation.ToolRole:
			toolParts := 0
			for _, part := range msg.Parts {
				if part.Type != conversation.ToolResultPartType {
					continue
				}
				toolParts++
				if strings.TrimSpace(part.ToolCallID) == "" {
					return nil, fmt.Errorf("message %d: tool result missing tool call id", i)
				}
				out = append(out, openai.ToolMessage(part.Content, part.ToolCallID))
			}
			if toolParts == 0 {
				return nil, fmt.Errorf("message %d: tool message missing tool result part", i)
			}
		default:
			return nil, fmt.Errorf("message %d: unsupported role %q", i, msg.Role)
		}
	}

	if droppedReplayParts > 0 {
		log.Printf(
			"q15: openai-compatible request dropped unmatched reasoning replay state parts=%d and continued without opaque replay",
			droppedReplayParts,
		)
	}

	return out, nil
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
				toolCalls = append(toolCalls, map[string]any{
					"id":   part.ID,
					"type": "function",
					"function": map[string]any{
						"name":      part.Name,
						"arguments": conversation.NormalizePart(part).Arguments,
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

	payload := map[string]any{
		"role":    "assistant",
		"content": strings.TrimSpace(text),
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
