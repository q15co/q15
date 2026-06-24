// Package ollama implements Ollama completion and model-roster adapters.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const (
	defaultBaseURL = "http://localhost:11434"

	ollamaReplayKey = "ollama"

	toolImageFollowupText      = "Use the attached image output from the previous tool result when continuing."
	assistantImageFollowupText = "Use the attached image output from the assistant's previous response when continuing."
	systemOnlyFollowupText     = "Use the system instructions above as the full task definition and complete them directly."
)

// Client adapts Ollama's native API to the agent model interface.
type Client struct {
	chat       chatAPI
	mediaStore q15media.Store
}

var _ agent.ModelClient = (*Client)(nil)

type chatAPI interface {
	Chat(context.Context, *ollamaapi.ChatRequest, ollamaapi.ChatResponseFunc) error
}

type nativeChatClient struct {
	client *ollamaapi.Client
}

func (c nativeChatClient) Chat(
	ctx context.Context,
	req *ollamaapi.ChatRequest,
	fn ollamaapi.ChatResponseFunc,
) error {
	return c.client.Chat(ctx, req, fn)
}

// NewClient constructs an Ollama model client. Empty baseURL defaults to the
// local Ollama daemon. apiKey is optional and enables direct Ollama Cloud API
// access through Bearer authentication.
func NewClient(baseURL string, apiKey string, mediaStore q15media.Store) (*Client, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	var chat chatAPI
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		chat = nativeChatClient{
			client: ollamaapi.NewClient(base, http.DefaultClient),
		}
	} else {
		chat = newBearerChatClient(base, apiKey, http.DefaultClient)
	}

	return &Client{
		chat:       chat,
		mediaStore: mediaStore,
	}, nil
}

// Complete sends one completion request to the configured Ollama endpoint.
func (c *Client) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	if strings.TrimSpace(model) == "" {
		return agent.ModelClientResult{}, fmt.Errorf("model name is required")
	}
	if c == nil || c.chat == nil {
		return agent.ModelClientResult{}, fmt.Errorf("ollama chat client is required")
	}

	reqMessages, err := mapMessages(withPromptProfile(messages), c.mediaStore)
	if err != nil {
		return agent.ModelClientResult{}, err
	}
	reqTools, err := mapTools(tools)
	if err != nil {
		return agent.ModelClientResult{}, err
	}

	stream := true
	req := &ollamaapi.ChatRequest{
		Model:    strings.TrimSpace(model),
		Messages: reqMessages,
		Stream:   &stream,
		Tools:    reqTools,
	}

	collector := newStreamCollector()
	if err := c.chat.Chat(ctx, req, collector.Record); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("ollama chat: %w", err)
	}

	return collector.Result()
}

func normalizeBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultBaseURL
	}

	base, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse ollama base url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("ollama base url must include scheme and host")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("ollama base url must not include query or fragment")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if base.Path == "/api" || strings.HasSuffix(base.Path, "/api") {
		base.Path = strings.TrimSuffix(base.Path, "/api")
	}
	return base, nil
}

func mapMessages(
	messages []conversation.Message,
	mediaStore q15media.Store,
) ([]ollamaapi.Message, error) {
	out := make([]ollamaapi.Message, 0, len(messages)+1)
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
	toolNamesByID := make(map[string]string)

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if needsBootstrap && !insertedBootstrap && msg.Role != conversation.SystemRole {
			out = append(out, ollamaapi.Message{
				Role:    "user",
				Content: systemOnlyFollowupText,
			})
			insertedBootstrap = true
		}

		switch msg.Role {
		case conversation.SystemRole:
			text, err := textOnlyMessageContent(msg)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, ollamaapi.Message{
					Role:    "system",
					Content: trimmed,
				})
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
			assistantMessage, ok, dropped, err := buildAssistantReplayMessage(group)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			}
			droppedReplayParts += dropped
			if ok {
				out = append(out, assistantMessage)
				for _, toolCall := range assistantMessage.ToolCalls {
					id := strings.TrimSpace(toolCall.ID)
					name := strings.TrimSpace(toolCall.Function.Name)
					if id != "" && name != "" {
						toolNamesByID[id] = name
					}
				}
			}
			if imageFollowup, hasImages, err := buildAssistantImageFollowupMessage(
				group,
				mediaStore,
			); err != nil {
				return nil, fmt.Errorf("message %d: %w", i, err)
			} else if hasImages {
				out = append(out, imageFollowup)
			}
		case conversation.ToolRole:
			toolParts := 0
			imageParts := make([]conversation.Part, 0, len(msg.Parts))
			for _, part := range conversation.NormalizeParts(msg.Parts) {
				switch part.Type {
				case conversation.ToolResultPartType:
					toolParts++
					toolCallID := strings.TrimSpace(part.ToolCallID)
					if toolCallID == "" {
						return nil, fmt.Errorf("message %d: tool result missing tool call id", i)
					}
					out = append(out, ollamaapi.Message{
						Role:       "tool",
						Content:    part.Content,
						ToolName:   toolNamesByID[toolCallID],
						ToolCallID: toolCallID,
					})
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
		out = append(out, ollamaapi.Message{
			Role:    "user",
			Content: systemOnlyFollowupText,
		})
	}

	if droppedReplayParts > 0 {
		log.Printf(
			"q15: ollama request dropped unmatched reasoning replay state parts=%d and continued with portable transcript fields",
			droppedReplayParts,
		)
	}

	return out, nil
}

func buildUserMessage(
	msg conversation.Message,
	mediaStore q15media.Store,
) (ollamaapi.Message, error) {
	msg = conversation.PromptVisibleUserMessage(msg)
	var content strings.Builder
	images := make([]ollamaapi.ImageData, 0)

	for _, part := range conversation.NormalizeParts(msg.Parts) {
		switch part.Type {
		case conversation.TextPartType:
			content.WriteString(part.Text)
		case conversation.ImagePartType:
			image, err := resolveImagePart(part, mediaStore)
			if err != nil {
				return ollamaapi.Message{}, fmt.Errorf("resolve image input: %w", err)
			}
			images = append(images, image)
		default:
			return ollamaapi.Message{}, fmt.Errorf(
				"unsupported user message part type %q",
				part.Type,
			)
		}
	}

	return ollamaapi.Message{
		Role:    "user",
		Content: content.String(),
		Images:  images,
	}, nil
}

func resolveImagePart(
	part conversation.Part,
	mediaStore q15media.Store,
) (ollamaapi.ImageData, error) {
	dataURL, err := q15media.ResolveImagePartDataURL(
		part,
		mediaStore,
		q15media.DefaultMaxImageBytes,
	)
	if err != nil {
		return nil, err
	}

	meta, encoded, ok := strings.Cut(dataURL, ",")
	if !ok || !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, fmt.Errorf("image data URL must be base64 encoded")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode image data URL: %w", err)
	}
	return ollamaapi.ImageData(raw), nil
}

func buildToolImageFollowupMessage(
	imageParts []conversation.Part,
	mediaStore q15media.Store,
) (ollamaapi.Message, error) {
	parts := make([]conversation.Part, 0, 1+len(imageParts))
	parts = append(parts, conversation.Text(toolImageFollowupText, ""))
	parts = append(parts, conversation.CloneParts(imageParts)...)
	return buildUserMessage(conversation.UserMessageParts(parts...), mediaStore)
}

func buildAssistantImageFollowupMessage(
	messages []conversation.Message,
	mediaStore q15media.Store,
) (ollamaapi.Message, bool, error) {
	imageParts := collectAssistantImageParts(messages)
	if len(imageParts) == 0 {
		return ollamaapi.Message{}, false, nil
	}
	parts := make([]conversation.Part, 0, 1+len(imageParts))
	parts = append(parts, conversation.Text(assistantImageFollowupText, ""))
	parts = append(parts, imageParts...)
	message, err := buildUserMessage(conversation.UserMessageParts(parts...), mediaStore)
	if err != nil {
		return ollamaapi.Message{}, false, err
	}
	return message, true, nil
}

func collectAssistantImageParts(messages []conversation.Message) []conversation.Part {
	out := make([]conversation.Part, 0)
	for _, msg := range messages {
		for _, part := range conversation.NormalizeParts(msg.Parts) {
			if part.Type != conversation.ImagePartType {
				continue
			}
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

func buildAssistantReplayMessage(
	messages []conversation.Message,
) (ollamaapi.Message, bool, int, error) {
	var content strings.Builder
	var thinking strings.Builder
	toolCalls := make([]ollamaapi.ToolCall, 0)
	droppedReplayParts := 0

	for _, msg := range messages {
		for _, part := range conversation.NormalizeParts(msg.Parts) {
			switch part.Type {
			case conversation.TextPartType:
				content.WriteString(part.Text)
			case conversation.ReasoningPartType:
				if text := strings.TrimSpace(part.Text); text != "" {
					if thinking.Len() > 0 {
						thinking.WriteString("\n")
					}
					thinking.WriteString(text)
				} else if len(part.Replay) > 0 && extractOllamaThinkingReplay(part.Replay) == "" {
					droppedReplayParts++
				}
				if replayThinking := extractOllamaThinkingReplay(part.Replay); replayThinking != "" &&
					!strings.Contains(thinking.String(), replayThinking) {
					if thinking.Len() > 0 {
						thinking.WriteString("\n")
					}
					thinking.WriteString(replayThinking)
				}
			case conversation.ToolCallPartType:
				toolCall, err := buildToolCall(part, len(toolCalls)+1)
				if err != nil {
					return ollamaapi.Message{}, false, droppedReplayParts, err
				}
				toolCalls = append(toolCalls, toolCall)
			}
		}
	}

	if strings.TrimSpace(content.String()) == "" && strings.TrimSpace(thinking.String()) == "" &&
		len(toolCalls) == 0 {
		return ollamaapi.Message{}, false, droppedReplayParts, nil
	}

	return ollamaapi.Message{
		Role:      "assistant",
		Content:   content.String(),
		Thinking:  strings.TrimSpace(thinking.String()),
		ToolCalls: toolCalls,
	}, true, droppedReplayParts, nil
}

func buildToolCall(part conversation.Part, fallbackIndex int) (ollamaapi.ToolCall, error) {
	if strings.TrimSpace(part.Name) == "" {
		return ollamaapi.ToolCall{}, fmt.Errorf("tool call name is required")
	}
	arguments := part.Arguments
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}

	var args ollamaapi.ToolCallFunctionArguments
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ollamaapi.ToolCall{}, fmt.Errorf("decode tool call arguments: %w", err)
	}

	id := strings.TrimSpace(part.ID)
	if id == "" {
		id = fmt.Sprintf("ollama-call-%d", fallbackIndex)
	}

	return ollamaapi.ToolCall{
		ID: id,
		Function: ollamaapi.ToolCallFunction{
			Name:      strings.TrimSpace(part.Name),
			Arguments: args,
		},
	}, nil
}

func extractOllamaThinkingReplay(replay map[string]json.RawMessage) string {
	raw := replay[ollamaReplayKey]
	if len(raw) == 0 {
		return ""
	}

	var probe struct {
		Thinking string `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Thinking)
}

func mapTools(tools []agent.ToolDefinition) (ollamaapi.Tools, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	out := make(ollamaapi.Tools, 0, len(tools))
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
		mappedParameters, err := mapToolParameters(parameters)
		if err != nil {
			return nil, fmt.Errorf("map tool %q parameters: %w", name, err)
		}

		out = append(out, ollamaapi.Tool{
			Type: "function",
			Function: ollamaapi.ToolFunction{
				Name:        name,
				Description: strings.TrimSpace(tool.Description),
				Parameters:  mappedParameters,
			},
		})
	}
	return out, nil
}

func mapToolParameters(parameters map[string]any) (ollamaapi.ToolFunctionParameters, error) {
	data, err := json.Marshal(parameters)
	if err != nil {
		return ollamaapi.ToolFunctionParameters{}, err
	}
	var out ollamaapi.ToolFunctionParameters
	if err := json.Unmarshal(data, &out); err != nil {
		return ollamaapi.ToolFunctionParameters{}, err
	}
	if strings.TrimSpace(out.Type) == "" {
		out.Type = "object"
	}
	if out.Properties == nil {
		out.Properties = ollamaapi.NewToolPropertiesMap()
	}
	return out, nil
}

type streamCollector struct {
	sawResponse bool
	content     strings.Builder
	thinking    strings.Builder
	toolCalls   []ollamaapi.ToolCall
	finish      string
}

func newStreamCollector() *streamCollector {
	return &streamCollector{}
}

func (c *streamCollector) Record(resp ollamaapi.ChatResponse) error {
	c.sawResponse = true
	c.content.WriteString(resp.Message.Content)
	c.thinking.WriteString(resp.Message.Thinking)
	if len(resp.Message.ToolCalls) > 0 {
		c.toolCalls = append(c.toolCalls, resp.Message.ToolCalls...)
	}
	if strings.TrimSpace(resp.DoneReason) != "" {
		c.finish = resp.DoneReason
	}
	return nil
}

func (c *streamCollector) Result() (agent.ModelClientResult, error) {
	if !c.sawResponse {
		return agent.ModelClientResult{}, fmt.Errorf("ollama chat returned no responses")
	}

	message := ollamaapi.Message{
		Role:      "assistant",
		Content:   c.content.String(),
		Thinking:  c.thinking.String(),
		ToolCalls: c.toolCalls,
	}
	messages, err := parseAssistantMessage(message)
	if err != nil {
		return agent.ModelClientResult{}, err
	}
	return agent.ModelClientResult{
		Messages:     messages,
		FinishReason: c.finish,
	}, nil
}

func parseAssistantMessage(message ollamaapi.Message) ([]conversation.Message, error) {
	parts := make([]conversation.Part, 0, 2+len(message.ToolCalls))
	if thinking := strings.TrimSpace(message.Thinking); thinking != "" {
		rawReplay, err := json.Marshal(map[string]string{"thinking": thinking})
		if err != nil {
			return nil, fmt.Errorf("marshal reasoning replay: %w", err)
		}
		parts = append(parts, conversation.Reasoning(thinking, map[string]json.RawMessage{
			ollamaReplayKey: rawReplay,
		}))
	}
	if content := strings.TrimSpace(message.Content); content != "" {
		parts = append(parts, conversation.Text(content, ""))
	}

	for i, toolCall := range message.ToolCalls {
		name := strings.TrimSpace(toolCall.Function.Name)
		if name == "" {
			return nil, fmt.Errorf("tool call %d missing function name", i)
		}
		id := strings.TrimSpace(toolCall.ID)
		if id == "" {
			id = fmt.Sprintf("ollama-call-%d", i+1)
		}
		parts = append(parts, conversation.ToolCall(
			id,
			name,
			toolCall.Function.Arguments.String(),
		))
	}

	if len(parts) == 0 {
		return nil, nil
	}
	return []conversation.Message{conversation.AssistantMessage(parts...)}, nil
}

type bearerChatClient struct {
	base       *url.URL
	apiKey     string
	httpClient *http.Client
}

func newBearerChatClient(base *url.URL, apiKey string, httpClient *http.Client) *bearerChatClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &bearerChatClient{
		base:       cloneURL(base),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

func (c *bearerChatClient) Chat(
	ctx context.Context,
	req *ollamaapi.ChatRequest,
	fn ollamaapi.ChatResponseFunc,
) error {
	if c == nil || c.base == nil {
		return fmt.Errorf("ollama bearer client base url is required")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return fmt.Errorf("ollama bearer client api key is required")
	}
	if fn == nil {
		return fmt.Errorf("ollama chat response callback is required")
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(req); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/api/chat"), &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("User-Agent", "q15-ollama-provider")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized {
		var authError ollamaapi.AuthorizationError
		_ = json.NewDecoder(response.Body).Decode(&authError)
		authError.StatusCode = response.StatusCode
		authError.Status = response.Status
		return authError
	}
	if response.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		return statusError(response, body)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), q15media.DefaultMaxImageBytes)
	for scanner.Scan() {
		var streamErr struct {
			Error string `json:"error,omitempty"`
		}
		line := scanner.Bytes()
		if err := json.Unmarshal(line, &streamErr); err == nil &&
			strings.TrimSpace(streamErr.Error) != "" {
			return errors.New(streamErr.Error)
		}

		var resp ollamaapi.ChatResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return err
		}
		if err := fn(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *bearerChatClient) endpoint(path string) string {
	endpoint := c.base.JoinPath(path)
	return endpoint.String()
}

func statusError(resp *http.Response, body []byte) error {
	apiError := ollamaapi.StatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &apiError); err != nil {
			apiError.ErrorMessage = string(body)
		}
	}
	return apiError
}

func cloneURL(in *url.URL) *url.URL {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
