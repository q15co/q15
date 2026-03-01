package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/auth"
)

const (
	codexBaseURL             = "https://chatgpt.com/backend-api/codex"
	codexOriginator          = "codex_cli_rs"
	codexBetaHeader          = "responses=experimental"
	defaultCodexInstructions = "You are Codex, a coding assistant."
)

type tokenSource func(context.Context) (token string, accountID string, err error)

type Client struct {
	client      openai.Client
	tokenSource tokenSource
}

var _ agent.ModelClient = (*Client)(nil)

func NewClient() (*Client, error) {
	client := openai.NewClient(
		option.WithBaseURL(codexBaseURL),
		option.WithHeader("originator", codexOriginator),
		option.WithHeader("OpenAI-Beta", codexBetaHeader),
	)
	return &Client{
		client:      client,
		tokenSource: auth.LoadOpenAIToken,
	}, nil
}

func (c *Client) Complete(
	ctx context.Context,
	model string,
	messages []agent.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	if strings.TrimSpace(model) == "" {
		return agent.ModelClientResult{}, fmt.Errorf("model name is required")
	}
	if c.tokenSource == nil {
		return agent.ModelClientResult{}, fmt.Errorf("token source is required")
	}

	token, accountID, err := c.tokenSource(ctx)
	if err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("resolve openai credential: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return agent.ModelClientResult{}, fmt.Errorf("openai credential has empty access token")
	}

	params, err := buildRequestParams(model, messages, tools)
	if err != nil {
		return agent.ModelClientResult{}, err
	}

	opts := []option.RequestOption{
		option.WithAPIKey(token),
	}
	if strings.TrimSpace(accountID) != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	}

	stream := c.client.Responses.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var resp *responses.Response
	for stream.Next() {
		evt := stream.Current()
		switch evt.Type {
		case "response.completed", "response.failed", "response.incomplete":
			if evt.Response.ID != "" {
				respCopy := evt.Response
				resp = &respCopy
			}
		}
	}
	if err := stream.Err(); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("responses api: %w", err)
	}
	if resp == nil {
		return agent.ModelClientResult{}, fmt.Errorf("responses api: stream ended without response")
	}

	result := parseResponse(resp)
	result.ProviderRaw = json.RawMessage(resp.RawJSON())
	return result, nil
}

func buildRequestParams(
	model string,
	messages []agent.Message,
	tools []agent.ToolDefinition,
) (responses.ResponseNewParams, error) {
	input, instructions, err := mapMessages(messages)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	if strings.TrimSpace(instructions) == "" {
		instructions = defaultCodexInstructions
	}

	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Instructions: openai.Opt(instructions),
		Store:        openai.Opt(false),
	}
	mappedTools := mapTools(tools)
	if len(mappedTools) > 0 {
		params.Tools = mappedTools
	}
	return params, nil
}

func mapMessages(messages []agent.Message) (responses.ResponseInputParam, string, error) {
	input := make(responses.ResponseInputParam, 0, len(messages))
	instructions := ""

	for i, msg := range messages {
		switch msg.Role {
		case agent.SystemRole:
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				instructions = trimmed
			}
		case agent.UserRole:
			if strings.TrimSpace(msg.ToolCallID) != "" {
				input = append(input, responses.ResponseInputItemUnionParam{
					OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
						CallID: msg.ToolCallID,
						Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
							OfString: openai.Opt(msg.Content),
						},
					},
				})
				continue
			}

			input = append(input, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfString: openai.Opt(msg.Content),
					},
				},
			})
		case agent.AssistantRole:
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				input = append(input, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role: responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{
							OfString: openai.Opt(trimmed),
						},
					},
				})
			}

			for _, tc := range msg.ToolCalls {
				name := strings.TrimSpace(tc.Name)
				if name == "" {
					return nil, "", fmt.Errorf("message %d: tool call name is required", i)
				}
				args := strings.TrimSpace(tc.Arguments)
				if args == "" {
					args = "{}"
				}
				input = append(input, responses.ResponseInputItemUnionParam{
					OfFunctionCall: &responses.ResponseFunctionToolCallParam{
						CallID:    tc.ID,
						Name:      name,
						Arguments: args,
					},
				})
			}
		case agent.ToolRole:
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, "", fmt.Errorf("message %d: tool message missing tool call id", i)
			}
			input = append(input, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: msg.ToolCallID,
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.Opt(msg.Content),
					},
				},
			})
		default:
			return nil, "", fmt.Errorf("message %d: unsupported role %q", i, msg.Role)
		}
	}

	return input, instructions, nil
}

func mapTools(tools []agent.ToolDefinition) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}

	out := make([]responses.ToolUnionParam, 0, len(tools))
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

		fnTool := responses.FunctionToolParam{
			Name:       name,
			Parameters: parameters,
			Strict:     openai.Opt(false),
		}
		if desc := strings.TrimSpace(tool.Description); desc != "" {
			fnTool.Description = openai.Opt(desc)
		}

		out = append(out, responses.ToolUnionParam{OfFunction: &fnTool})
	}
	return out
}

func parseResponse(resp *responses.Response) agent.ModelClientResult {
	if resp == nil {
		return agent.ModelClientResult{
			FinishReason: "stop",
		}
	}

	content := strings.TrimSpace(resp.OutputText())
	var toolCalls []agent.ToolCall
	if len(resp.Output) > 0 {
		toolCalls = make([]agent.ToolCall, 0)
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			if content != "" {
				continue
			}
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					content = strings.TrimSpace(content + part.Text)
				case "refusal":
					if content == "" {
						content = strings.TrimSpace(part.Refusal)
					}
				}
			}
		case "function_call":
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			args := strings.TrimSpace(item.Arguments)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, agent.ToolCall{
				ID:        item.CallID,
				Name:      name,
				Arguments: args,
			})
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	if resp.Status == "incomplete" {
		finishReason = "length"
	}

	return agent.ModelClientResult{
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
	}
}
