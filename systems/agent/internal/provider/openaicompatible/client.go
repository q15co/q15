// Package openaicompatible implements an OpenAI Chat Completions-compatible
// model client.
package openaicompatible

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

// Client adapts an OpenAI-compatible Chat Completions API to the agent model
// interface.
type Client struct {
	client openai.Client
}

var _ agent.ModelClient = (*Client)(nil)

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
	messages []agent.Message,
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
	toolCalls, err := mapToolCalls(choice.Message.ToolCalls)
	if err != nil {
		return agent.ModelClientResult{}, err
	}

	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		content = strings.TrimSpace(choice.Message.Refusal)
	}

	return agent.ModelClientResult{
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: choice.FinishReason,
		ProviderRaw:  json.RawMessage(choice.Message.RawJSON()),
	}, nil
}

func mapMessages(messages []agent.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))

	for i, msg := range messages {
		switch msg.Role {
		case agent.SystemRole:
			out = append(out, openai.SystemMessage(msg.Content))
		case agent.UserRole:
			out = append(out, openai.UserMessage(msg.Content))
		case agent.AssistantRole:
			if len(msg.ProviderRaw) > 0 && isAssistantProviderRaw(msg.ProviderRaw) {
				out = append(
					out,
					param.Override[openai.ChatCompletionMessageParamUnion](msg.ProviderRaw),
				)
				continue
			}

			toolCalls := mapAssistantToolCalls(msg.ToolCalls)
			if len(toolCalls) == 0 {
				out = append(out, openai.AssistantMessage(msg.Content))
				continue
			}

			assistant := openai.ChatCompletionAssistantMessageParam{
				ToolCalls: toolCalls,
			}
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				assistant.Content.OfString = param.NewOpt(trimmed)
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistant,
			})
		case agent.ToolRole:
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, fmt.Errorf("message %d: tool message missing tool call id", i)
			}
			out = append(out, openai.ToolMessage(msg.Content, msg.ToolCallID))
		default:
			return nil, fmt.Errorf("message %d: unsupported role %q", i, msg.Role)
		}
	}

	return out, nil
}

func mapAssistantToolCalls(
	calls []agent.ToolCall,
) []openai.ChatCompletionMessageToolCallUnionParam {
	if len(calls) == 0 {
		return nil
	}

	out := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(calls))
	for _, call := range calls {
		out = append(out, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: call.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			},
		})
	}
	return out
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

func mapToolCalls(toolCalls []openai.ChatCompletionMessageToolCallUnion) ([]agent.ToolCall, error) {
	if len(toolCalls) == 0 {
		return nil, nil
	}

	out := make([]agent.ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		switch call := toolCall.AsAny().(type) {
		case openai.ChatCompletionMessageFunctionToolCall:
			out = append(out, agent.ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			})
		default:
			return nil, fmt.Errorf("unsupported tool call type %q", toolCall.Type)
		}
	}

	return out, nil
}

func isAssistantProviderRaw(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(probe.Role), "assistant")
}
