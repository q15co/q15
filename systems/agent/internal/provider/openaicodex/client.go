// Package openaicodex implements the OpenAI Codex-backed model client.
package openaicodex

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const (
	codexBaseURL    = "https://chatgpt.com/backend-api/codex"
	codexOriginator = "codex_cli_rs"
	codexBetaHeader = "responses=experimental"

	openAIResponsesReplayKey = "openai_responses"
	toolImageFollowupText    = "Use the attached image output from the previous tool result when continuing."
	systemOnlyInputText      = "Use the system instructions above as the full task definition and complete them directly."
)

type tokenSource func(context.Context) (token string, accountID string, err error)

// Client adapts the OpenAI Codex responses API to the agent model interface.
type Client struct {
	client      openai.Client
	mediaStore  q15media.Store
	tokenSource tokenSource
}

var _ agent.ModelClient = (*Client)(nil)

// NewClient constructs a Codex-backed model client with q15 defaults.
func NewClient(mediaStore q15media.Store) (*Client, error) {
	client := openai.NewClient(
		option.WithBaseURL(codexBaseURL),
		option.WithHeader("originator", codexOriginator),
		option.WithHeader("OpenAI-Beta", codexBetaHeader),
	)
	return &Client{
		client:      client,
		mediaStore:  mediaStore,
		tokenSource: auth.LoadOpenAIToken,
	}, nil
}

// Complete sends a completion request to the Codex responses API.
func (c *Client) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
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

	params, err := buildRequestParams(model, messages, tools, c.mediaStore)
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
		return agent.ModelClientResult{}, fmt.Errorf(
			"responses api: %s",
			formatResponsesAPIError(resp, streamEventErr, err.Error()),
		)
	}
	if resp == nil {
		return agent.ModelClientResult{}, fmt.Errorf(
			"responses api: %s",
			formatResponsesAPIError(nil, streamEventErr, "stream ended without response"),
		)
	}
	if err := validateFinalResponse(resp); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf(
			"responses api: %s",
			formatResponsesAPIError(resp, streamEventErr, err.Error()),
		)
	}

	result := parseResponse(resp)
	result = mergeResultWithStreamSnapshot(result, snapshot)
	return result, nil
}

func buildRequestParams(
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	mediaStore q15media.Store,
) (responses.ResponseNewParams, error) {
	input, instructions, err := mapMessages(messages, mediaStore)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	if strings.TrimSpace(instructions) == "" {
		instructions = agent.DefaultSystemPrompt
	}
	instructions = appendPromptProfileInstructions(instructions)
	if len(input) == 0 {
		input = append(input, responses.ResponseInputItemParamOfMessage(
			systemOnlyInputText,
			responses.EasyInputMessageRoleUser,
		))
	}

	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Instructions: openai.Opt(instructions),
		Store:        openai.Opt(false),
		Include: []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		},
		Reasoning: shared.ReasoningParam{
			Summary: shared.ReasoningSummaryAuto,
		},
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

func mapMessages(
	messages []conversation.Message,
	mediaStore q15media.Store,
) (responses.ResponseInputParam, string, error) {
	input := make(responses.ResponseInputParam, 0, len(messages))
	instructionsParts := make([]string, 0, 1)
	droppedReplayParts := 0

	for i, msg := range messages {
		switch msg.Role {
		case conversation.SystemRole:
			text, err := textOnlyMessageContent(msg)
			if err != nil {
				return nil, "", fmt.Errorf("message %d: %w", i, err)
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				instructionsParts = append(instructionsParts, trimmed)
			}
		case conversation.UserRole:
			item, err := buildUserInputItem(msg, mediaStore)
			if err != nil {
				return nil, "", fmt.Errorf("message %d: %w", i, err)
			}
			input = append(input, item)
		case conversation.AssistantRole:
			for _, part := range msg.Parts {
				part = conversation.NormalizePart(part)
				switch part.Type {
				case conversation.TextPartType:
					raw, ok, err := buildAssistantTextReplayMessage(part)
					if err != nil {
						return nil, "", fmt.Errorf("message %d: %w", i, err)
					}
					if ok {
						input = append(
							input,
							param.Override[responses.ResponseInputItemUnionParam](raw),
						)
					}
				case conversation.ReasoningPartType:
					raw, ok, dropped, err := buildReasoningReplayItem(part)
					if err != nil {
						return nil, "", fmt.Errorf("message %d: %w", i, err)
					}
					droppedReplayParts += dropped
					if ok {
						input = append(
							input,
							param.Override[responses.ResponseInputItemUnionParam](raw),
						)
					}
				case conversation.ToolCallPartType:
					if strings.TrimSpace(part.Name) == "" {
						return nil, "", fmt.Errorf("message %d: tool call name is required", i)
					}
					input = append(input, responses.ResponseInputItemUnionParam{
						OfFunctionCall: &responses.ResponseFunctionToolCallParam{
							CallID:    part.ID,
							Name:      part.Name,
							Arguments: part.Arguments,
						},
					})
				}
			}
		case conversation.ToolRole:
			toolParts := 0
			imageParts := make([]conversation.Part, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				part = conversation.NormalizePart(part)
				switch part.Type {
				case conversation.ToolResultPartType:
					toolParts++
					if strings.TrimSpace(part.ToolCallID) == "" {
						return nil, "", fmt.Errorf(
							"message %d: tool result missing tool call id",
							i,
						)
					}
					input = append(input, responses.ResponseInputItemUnionParam{
						OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
							CallID: part.ToolCallID,
							Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
								OfString: openai.Opt(part.Content),
							},
						},
					})
				case conversation.ImagePartType:
					imageParts = append(imageParts, part)
				}
			}
			if toolParts == 0 && len(imageParts) == 0 {
				return nil, "", fmt.Errorf("message %d: tool message missing tool result part", i)
			}
			if len(imageParts) > 0 {
				item, err := buildToolImageFollowupInputItem(imageParts, mediaStore)
				if err != nil {
					return nil, "", fmt.Errorf("message %d: %w", i, err)
				}
				input = append(input, item)
			}
		default:
			return nil, "", fmt.Errorf("message %d: unsupported role %q", i, msg.Role)
		}
	}

	if droppedReplayParts > 0 {
		log.Printf(
			"q15: openai-responses request dropped unmatched reasoning replay state parts=%d and continued without opaque replay",
			droppedReplayParts,
		)
	}

	return input, strings.Join(instructionsParts, "\n\n"), nil
}

func buildUserInputItem(
	msg conversation.Message,
	mediaStore q15media.Store,
) (responses.ResponseInputItemUnionParam, error) {
	contentParts := make(responses.ResponseInputMessageContentListParam, 0, len(msg.Parts))
	textOnly := true
	var textBuilder strings.Builder

	for _, part := range conversation.NormalizeParts(msg.Parts) {
		switch part.Type {
		case conversation.TextPartType:
			textBuilder.WriteString(part.Text)
			contentParts = append(
				contentParts,
				responses.ResponseInputContentParamOfInputText(part.Text),
			)
		case conversation.ImagePartType:
			textOnly = false
			dataURL, err := q15media.ResolveImagePartDataURL(
				part,
				mediaStore,
				q15media.DefaultMaxImageBytes,
			)
			if err != nil {
				return responses.ResponseInputItemUnionParam{}, fmt.Errorf(
					"resolve image input: %w",
					err,
				)
			}
			imagePart := responses.ResponseInputContentParamOfInputImage(
				responses.ResponseInputImageDetailAuto,
			)
			imagePart.OfInputImage.ImageURL = openai.Opt(dataURL)
			contentParts = append(contentParts, imagePart)
		default:
			return responses.ResponseInputItemUnionParam{}, fmt.Errorf(
				"unsupported user message part type %q",
				part.Type,
			)
		}
	}

	if textOnly {
		return responses.ResponseInputItemParamOfMessage(
			textBuilder.String(),
			responses.EasyInputMessageRoleUser,
		), nil
	}
	return responses.ResponseInputItemParamOfMessage(
		contentParts,
		responses.EasyInputMessageRoleUser,
	), nil
}

func buildToolImageFollowupInputItem(
	imageParts []conversation.Part,
	mediaStore q15media.Store,
) (responses.ResponseInputItemUnionParam, error) {
	parts := make([]conversation.Part, 0, 1+len(imageParts))
	parts = append(parts, conversation.Text(toolImageFollowupText, ""))
	parts = append(parts, conversation.CloneParts(imageParts)...)
	return buildUserInputItem(conversation.UserMessageParts(parts...), mediaStore)
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

	messages := make([]conversation.Message, 0, len(resp.Output))
	toolCalls := 0

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			if msg, ok := parseAssistantMessageItem(item); ok {
				messages = append(messages, msg)
			}
		case "reasoning":
			if msg, ok := parseReasoningItem(item); ok {
				messages = append(messages, msg)
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
			toolCalls++
			messages = append(messages, conversation.AssistantMessage(
				conversation.ToolCall(item.CallID, name, args),
			))
		}
	}

	finishReason := "stop"
	if toolCalls > 0 {
		finishReason = "tool_calls"
	}
	if resp.Status == responses.ResponseStatusIncomplete {
		finishReason = "length"
	}

	return agent.ModelClientResult{
		Messages:     messages,
		FinishReason: finishReason,
	}
}

func buildAssistantTextReplayMessage(part conversation.Part) (json.RawMessage, bool, error) {
	text := strings.TrimSpace(part.Text)
	if text == "" {
		return nil, false, nil
	}

	payload := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{
			{
				"type": "output_text",
				"text": text,
			},
		},
	}
	switch part.Disposition {
	case conversation.TextDispositionCommentary:
		payload["phase"] = "commentary"
	case conversation.TextDispositionFinal:
		payload["phase"] = "final_answer"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal assistant text replay message: %w", err)
	}
	return raw, true, nil
}

func buildReasoningReplayItem(part conversation.Part) (json.RawMessage, bool, int, error) {
	if raw := part.Replay[openAIResponsesReplayKey]; len(raw) > 0 {
		return append(json.RawMessage(nil), raw...), true, 0, nil
	}
	if strings.TrimSpace(part.Text) == "" {
		if len(part.Replay) > 0 {
			return nil, false, 1, nil
		}
		return nil, false, 0, nil
	}

	payload := map[string]any{
		"type": "reasoning",
		"summary": []map[string]any{
			{
				"type": "summary_text",
				"text": strings.TrimSpace(part.Text),
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, false, 0, fmt.Errorf("marshal reasoning replay item: %w", err)
	}
	return raw, true, 0, nil
}

func parseAssistantMessageItem(
	item responses.ResponseOutputItemUnion,
) (conversation.Message, bool) {
	disposition := responseDisposition(item.RawJSON())
	parts := make([]conversation.Part, 0, len(item.Content))
	for _, part := range item.Content {
		switch part.Type {
		case "output_text":
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			parts = append(parts, conversation.Text(part.Text, disposition))
		case "refusal":
			if strings.TrimSpace(part.Refusal) == "" {
				continue
			}
			parts = append(parts, conversation.Text(part.Refusal, disposition))
		}
	}
	if len(parts) == 0 {
		return conversation.Message{}, false
	}
	return conversation.AssistantMessage(parts...), true
}

func parseReasoningItem(item responses.ResponseOutputItemUnion) (conversation.Message, bool) {
	text := extractReasoningText(item)
	replay := map[string]json.RawMessage(nil)
	if raw := strings.TrimSpace(item.RawJSON()); raw != "" {
		replay = map[string]json.RawMessage{
			openAIResponsesReplayKey: json.RawMessage(raw),
		}
	}
	if strings.TrimSpace(text) == "" && len(replay) == 0 {
		return conversation.Message{}, false
	}
	return conversation.AssistantMessage(conversation.Reasoning(text, replay)), true
}

func extractReasoningText(item responses.ResponseOutputItemUnion) string {
	var builder strings.Builder
	for _, part := range item.Summary {
		if text := strings.TrimSpace(part.Text); text != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text)
		}
	}
	if builder.Len() > 0 {
		return builder.String()
	}

	raw := strings.TrimSpace(item.RawJSON())
	if raw == "" {
		return ""
	}
	var probe struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	for _, part := range probe.Content {
		if part.Type != "reasoning_text" {
			continue
		}
		if text := strings.TrimSpace(part.Text); text != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func responseDisposition(raw string) conversation.TextDisposition {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var probe struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	switch strings.TrimSpace(probe.Phase) {
	case "commentary":
		return conversation.TextDispositionCommentary
	case "final_answer":
		return conversation.TextDispositionFinal
	default:
		return ""
	}
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

type streamSnapshot struct {
	textBuilder         strings.Builder
	refusalBuilder      strings.Builder
	toolCalls           []agent.ToolCall
	reasoningSummaries  map[string]*strings.Builder
	seenToolCallKeys    map[string]struct{}
	reasoningSummaryIDs map[string]struct{}
	textDeltaItemIDs    map[string]struct{}
	refusalDeltaItemIDs map[string]struct{}
}

func newStreamSnapshot() *streamSnapshot {
	return &streamSnapshot{
		reasoningSummaries:  make(map[string]*strings.Builder),
		seenToolCallKeys:    make(map[string]struct{}),
		reasoningSummaryIDs: make(map[string]struct{}),
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
	case "response.reasoning_summary_text.delta":
		if evt.ItemID != "" {
			s.reasoningSummaryIDs[evt.ItemID] = struct{}{}
		}
		s.reasoningSummaryBuilder(evt.ItemID).WriteString(evt.Delta)
	case "response.reasoning_summary_text.done":
		if evt.ItemID != "" {
			if _, hasDelta := s.reasoningSummaryIDs[evt.ItemID]; hasDelta {
				break
			}
		}
		s.reasoningSummaryBuilder(evt.ItemID).WriteString(evt.Text)
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

func (s *streamSnapshot) reasoningSummaryBuilder(itemID string) *strings.Builder {
	if s == nil {
		return &strings.Builder{}
	}
	builder, ok := s.reasoningSummaries[itemID]
	if ok {
		return builder
	}
	builder = &strings.Builder{}
	s.reasoningSummaries[itemID] = builder
	return builder
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

	for i := range result.Messages {
		for j := range result.Messages[i].Parts {
			part := result.Messages[i].Parts[j]
			if part.Type != conversation.ReasoningPartType || strings.TrimSpace(part.Text) != "" {
				continue
			}
			if summary := snapshot.ReasoningSummary(part); summary != "" {
				result.Messages[i].Parts[j].Text = summary
			}
		}
	}

	if strings.TrimSpace(conversation.FinalAnswer(result.Messages)) == "" {
		if text := snapshot.Text(); text != "" {
			result.Messages = append(result.Messages, conversation.AssistantMessage(
				conversation.Text(text, ""),
			))
		} else if refusal := snapshot.Refusal(); refusal != "" {
			result.Messages = append(result.Messages, conversation.AssistantMessage(
				conversation.Text(refusal, ""),
			))
		}
	}
	if len(agentToolCalls(result.Messages)) == 0 && len(snapshot.toolCalls) > 0 {
		for _, call := range snapshot.toolCalls {
			result.Messages = append(result.Messages, conversation.AssistantMessage(
				conversation.ToolCall(call.ID, call.Name, call.Arguments),
			))
		}
		if result.FinishReason == "" || result.FinishReason == "stop" {
			result.FinishReason = "tool_calls"
		}
	}
	return result
}

func (s *streamSnapshot) ReasoningSummary(part conversation.Part) string {
	if s == nil {
		return ""
	}
	raw := part.Replay[openAIResponsesReplayKey]
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	builder := s.reasoningSummaries[strings.TrimSpace(probe.ID)]
	if builder == nil {
		return ""
	}
	return strings.TrimSpace(builder.String())
}

func agentToolCalls(messages []conversation.Message) []agent.ToolCall {
	var out []agent.ToolCall
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type != conversation.ToolCallPartType {
				continue
			}
			out = append(out, agent.ToolCall{
				ID:        part.ID,
				Name:      part.Name,
				Arguments: part.Arguments,
			})
		}
	}
	return out
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

func formatResponsesAPIError(
	resp *responses.Response,
	streamEventErr string,
	fallback string,
) string {
	parts := make([]string, 0, 5)
	if resp != nil {
		if responseID := strings.TrimSpace(resp.ID); responseID != "" {
			parts = append(parts, fmt.Sprintf("response_id=%q", responseID))
		}
		if status := strings.TrimSpace(string(resp.Status)); status != "" {
			parts = append(parts, fmt.Sprintf("status=%q", status))
		}
	}

	responseErr := ""
	if resp != nil {
		switch resp.Status {
		case responses.ResponseStatusFailed, responses.ResponseStatusCancelled:
			responseErr = strings.TrimSpace(responseFailureDetail(resp))
			if responseErr == "unknown error" {
				responseErr = ""
			}
		}
	}
	if responseErr != "" {
		parts = append(parts, fmt.Sprintf("response_error=%q", responseErr))
	}

	streamEventErr = strings.TrimSpace(streamEventErr)
	if streamEventErr != "" {
		parts = append(parts, fmt.Sprintf("stream_error=%q", streamEventErr))
	}

	fallback = strings.TrimSpace(fallback)
	if fallback != "" && fallback != responseErr && fallback != streamEventErr {
		parts = append(parts, fmt.Sprintf("detail=%q", fallback))
	}

	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, " ")
}
