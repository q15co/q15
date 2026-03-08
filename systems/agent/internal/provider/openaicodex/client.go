// Package openaicodex implements the OpenAI Codex-backed model client.
package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/auth"
)

const (
	codexBaseURL    = "https://chatgpt.com/backend-api/codex"
	codexOriginator = "codex_cli_rs"
	codexBetaHeader = "responses=experimental"
)

type tokenSource func(context.Context) (token string, accountID string, err error)

// Client adapts the OpenAI Codex responses API to the agent model interface.
type Client struct {
	client      openai.Client
	tokenSource tokenSource
}

var _ agent.ModelClient = (*Client)(nil)

// NewClient constructs a Codex-backed model client with q15 defaults.
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

// Complete sends a completion request to the Codex responses API.
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
	streamEventErr := ""
	snapshot := newStreamSnapshot()
	for stream.Next() {
		evt := stream.Current()
		snapshot.Record(evt)
		switch evt.Type {
		case "response.done", "response.completed", "response.failed", "response.incomplete":
			if isStreamResponsePresent(evt.Response) {
				respCopy := evt.Response
				resp = &respCopy
			}
		case "error":
			streamEventErr = strings.TrimSpace(evt.Message)
			if streamEventErr == "" {
				streamEventErr = strings.TrimSpace(evt.Code)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("responses api: %w", err)
	}
	if resp == nil {
		if streamEventErr != "" {
			return agent.ModelClientResult{}, fmt.Errorf("responses api: %s", streamEventErr)
		}
		return agent.ModelClientResult{}, fmt.Errorf("responses api: stream ended without response")
	}
	if err := validateFinalResponse(resp); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("responses api: %w", err)
	}

	result := parseResponse(resp)
	result = mergeResultWithStreamSnapshot(result, snapshot)
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
		instructions = agent.DefaultSystemPrompt
	}
	instructions = appendPromptProfileInstructions(instructions)

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
		params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: openai.Opt(responses.ToolChoiceOptionsAuto),
		}
		params.ParallelToolCalls = openai.Opt(true)
	}
	return params, nil
}

func mapMessages(messages []agent.Message) (responses.ResponseInputParam, string, error) {
	input := make(responses.ResponseInputParam, 0, len(messages))
	instructionsParts := make([]string, 0, 1)

	for i, msg := range messages {
		switch msg.Role {
		case agent.SystemRole:
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				instructionsParts = append(instructionsParts, trimmed)
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
			if raw, ok, err := buildAssistantReplayMessage(msg); err != nil {
				return nil, "", err
			} else if ok {
				input = append(input, param.Override[responses.ResponseInputItemUnionParam](raw))
			} else if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
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

	return input, strings.Join(instructionsParts, "\n\n"), nil
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
	assistantPhase := ""
	var assistantRaw json.RawMessage
	var toolCalls []agent.ToolCall
	if len(resp.Output) > 0 {
		toolCalls = make([]agent.ToolCall, 0)
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			if raw, phase, ok := responseAssistantMessageRaw(item); ok {
				assistantRaw = raw
				if phase != "" {
					assistantPhase = phase
				}
			}
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
	if resp.Status == responses.ResponseStatusIncomplete {
		finishReason = "length"
	}

	return agent.ModelClientResult{
		Content:      content,
		Phase:        assistantPhase,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		ProviderRaw:  assistantRaw,
	}
}

func buildAssistantReplayMessage(msg agent.Message) (json.RawMessage, bool, error) {
	phase := strings.TrimSpace(msg.Phase)
	if phase == "" {
		return nil, false, nil
	}

	content := strings.TrimSpace(msg.Content)
	if content == "" {
		if isAssistantResponseOutputMessage(msg.ProviderRaw) {
			return append(json.RawMessage(nil), msg.ProviderRaw...), true, nil
		}
		return nil, false, nil
	}

	payload := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{
			{
				"type": "output_text",
				"text": content,
			},
		},
		"phase": phase,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal assistant replay message: %w", err)
	}
	return raw, true, nil
}

func responseAssistantMessageRaw(
	item responses.ResponseOutputItemUnion,
) (json.RawMessage, string, bool) {
	raw := strings.TrimSpace(item.RawJSON())
	if raw == "" {
		return nil, "", false
	}

	var probe struct {
		Type  string `json:"type"`
		Role  string `json:"role"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, "", false
	}
	if !strings.EqualFold(strings.TrimSpace(probe.Type), "message") {
		return nil, "", false
	}
	if !strings.EqualFold(strings.TrimSpace(probe.Role), "assistant") {
		return nil, "", false
	}
	return json.RawMessage(raw), strings.TrimSpace(probe.Phase), true
}

func isAssistantResponseOutputMessage(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var probe struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(probe.Type), "message") &&
		strings.EqualFold(strings.TrimSpace(probe.Role), "assistant")
}

type streamSnapshot struct {
	textBuilder         strings.Builder
	refusalBuilder      strings.Builder
	toolCalls           []agent.ToolCall
	seenToolCallKeys    map[string]struct{}
	textDeltaItemIDs    map[string]struct{}
	refusalDeltaItemIDs map[string]struct{}
}

func newStreamSnapshot() *streamSnapshot {
	return &streamSnapshot{
		seenToolCallKeys:    make(map[string]struct{}),
		textDeltaItemIDs:    make(map[string]struct{}),
		refusalDeltaItemIDs: make(map[string]struct{}),
	}
}

func (s *streamSnapshot) Record(evt responses.ResponseStreamEventUnion) {
	if s == nil {
		return
	}

	switch evt.Type {
	case "response.output_text.delta":
		if evt.ItemID != "" {
			s.textDeltaItemIDs[evt.ItemID] = struct{}{}
		}
		s.textBuilder.WriteString(evt.Delta)
	case "response.output_text.done":
		if evt.ItemID != "" {
			if _, hasDelta := s.textDeltaItemIDs[evt.ItemID]; hasDelta {
				break
			}
		}
		s.textBuilder.WriteString(evt.Text)
	case "response.refusal.delta":
		if evt.ItemID != "" {
			s.refusalDeltaItemIDs[evt.ItemID] = struct{}{}
		}
		s.refusalBuilder.WriteString(evt.Delta)
	case "response.refusal.done":
		if evt.ItemID != "" {
			if _, hasDelta := s.refusalDeltaItemIDs[evt.ItemID]; hasDelta {
				break
			}
		}
		s.refusalBuilder.WriteString(evt.Refusal)
	case "response.output_item.done":
		if evt.Item.Type != "function_call" {
			break
		}
		name := strings.TrimSpace(evt.Item.Name)
		if name == "" {
			break
		}
		args := strings.TrimSpace(evt.Item.Arguments)
		if args == "" {
			args = "{}"
		}
		callID := strings.TrimSpace(evt.Item.CallID)
		toolKey := strings.TrimSpace(evt.Item.ID)
		if toolKey == "" {
			toolKey = callID + "|" + name
		}
		if toolKey != "" {
			if _, seen := s.seenToolCallKeys[toolKey]; seen {
				break
			}
			s.seenToolCallKeys[toolKey] = struct{}{}
		}
		s.toolCalls = append(s.toolCalls, agent.ToolCall{
			ID:        callID,
			Name:      name,
			Arguments: args,
		})
	}
}

func (s *streamSnapshot) Text() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.textBuilder.String())
}

func (s *streamSnapshot) Refusal() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.refusalBuilder.String())
}

func mergeResultWithStreamSnapshot(
	result agent.ModelClientResult,
	snapshot *streamSnapshot,
) agent.ModelClientResult {
	if snapshot == nil {
		return result
	}

	if strings.TrimSpace(result.Content) == "" {
		if text := snapshot.Text(); text != "" {
			result.Content = text
		} else if refusal := snapshot.Refusal(); refusal != "" {
			result.Content = refusal
		}
	}
	if len(result.ToolCalls) == 0 && len(snapshot.toolCalls) > 0 {
		result.ToolCalls = append(result.ToolCalls, snapshot.toolCalls...)
		if result.FinishReason == "" || result.FinishReason == "stop" {
			result.FinishReason = "tool_calls"
		}
	}
	return result
}

func isStreamResponsePresent(resp responses.Response) bool {
	return resp.ID != "" ||
		resp.Status != "" ||
		len(resp.Output) > 0 ||
		strings.TrimSpace(resp.Error.Message) != ""
}

func validateFinalResponse(resp *responses.Response) error {
	if resp == nil {
		return fmt.Errorf("missing response")
	}

	switch resp.Status {
	case responses.ResponseStatusCompleted, responses.ResponseStatusIncomplete:
		return nil
	case responses.ResponseStatusFailed:
		return fmt.Errorf("response failed: %s", responseFailureDetail(resp))
	case responses.ResponseStatusCancelled:
		return fmt.Errorf("response cancelled: %s", responseFailureDetail(resp))
	case responses.ResponseStatusQueued, responses.ResponseStatusInProgress:
		return fmt.Errorf("response not finalized (status=%s)", resp.Status)
	case "":
		return fmt.Errorf("response missing status")
	default:
		return fmt.Errorf("unsupported response status %q", resp.Status)
	}
}

func responseFailureDetail(resp *responses.Response) string {
	if resp == nil {
		return "unknown error"
	}
	if msg := strings.TrimSpace(resp.Error.Message); msg != "" {
		return msg
	}
	if code := strings.TrimSpace(string(resp.Error.Code)); code != "" {
		return code
	}
	return "unknown error"
}
