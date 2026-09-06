package openaicompatible

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

var _ agent.StreamingModelClient = (*Client)(nil)

// CompleteStream streams assistant content synchronously, then returns the same
// canonical response as Complete. Reasoning and tool arguments stay private to
// the provider until the entire response has been assembled successfully.
func (c *Client) CompleteStream(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	onDelta func(string),
) (agent.ModelClientResult, error) {
	if strings.TrimSpace(model) == "" {
		return agent.ModelClientResult{}, fmt.Errorf("model name is required")
	}
	reqMessages, err := mapMessages(withPromptProfile(messages), c.mediaStore)
	if err != nil {
		return agent.ModelClientResult{}, err
	}
	params := openai.ChatCompletionNewParams{
		Messages: reqMessages,
		Model:    model,
		Tools:    mapTools(tools),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	var response *http.Response
	if err := c.client.Post(ctx, "chat/completions", params, &response,
		option.WithJSONSet("stream", true),
		option.WithHeader("Accept", "text/event-stream"),
	); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("chat completion stream: %w", err)
	}

	// The SDK's high-level stream treats an ordinary EOF as success and hides
	// [DONE]. Use its decoder directly so interrupted replies cannot be committed.
	decoder := ssestream.NewDecoder(response)
	defer decoder.Close()
	var accumulated completionStream
	for decoder.Next() {
		if err := ctx.Err(); err != nil {
			return agent.ModelClientResult{}, fmt.Errorf("chat completion stream: %w", err)
		}
		event := decoder.Event()
		data := bytes.TrimSpace(event.Data)
		if event.Type == "error" {
			return agent.ModelClientResult{}, fmt.Errorf(
				"chat completion stream provider error: %s",
				data,
			)
		}
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			return accumulated.result()
		}
		if err := accumulated.add(data, onDelta); err != nil {
			return agent.ModelClientResult{}, fmt.Errorf("chat completion stream: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("chat completion stream: %w", err)
	}
	if err := decoder.Err(); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("chat completion stream: %w", err)
	}
	return agent.ModelClientResult{}, fmt.Errorf(
		"chat completion stream missing [DONE]: %w",
		io.ErrUnexpectedEOF,
	)
}

type completionStream struct {
	content          strings.Builder
	refusal          strings.Builder
	reasoningContent strings.Builder
	reasoningOpaque  strings.Builder
	toolCalls        map[int64]*toolCallFragments
	finishReason     string
	usage            agent.ModelUsage
}

type streamToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolCallFragments struct {
	id        string
	kind      string
	name      strings.Builder
	arguments strings.Builder
}

func (s *completionStream) add(data []byte, onDelta func(string)) error {
	var chunk struct {
		Error   json.RawMessage         `json:"error"`
		Usage   *openai.CompletionUsage `json:"usage"`
		Choices []struct {
			Index        int64  `json:"index"`
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content          string `json:"content"`
				Refusal          string `json:"refusal"`
				ReasoningContent string `json:"reasoning_content"`
				ReasoningOpaque  string `json:"reasoning_opaque"`
				ToolCalls        []struct {
					Index int64 `json:"index"`
					streamToolCall
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("decode chunk: %w", err)
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
		return fmt.Errorf("provider error: %s", chunk.Error)
	}
	if chunk.Usage != nil {
		s.usage = modelUsage(*chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		// Like batch completions, only the first generated choice is returned.
		if choice.Index != 0 {
			continue
		}
		if s.finishReason != "" {
			return fmt.Errorf("received choice after finish reason %q", s.finishReason)
		}
		delta := choice.Delta
		s.content.WriteString(delta.Content)
		s.refusal.WriteString(delta.Refusal)
		s.reasoningContent.WriteString(delta.ReasoningContent)
		s.reasoningOpaque.WriteString(delta.ReasoningOpaque)
		for _, fragment := range delta.ToolCalls {
			if fragment.Index < 0 {
				return fmt.Errorf("negative tool call index %d", fragment.Index)
			}
			if s.toolCalls == nil {
				s.toolCalls = make(map[int64]*toolCallFragments)
			}
			call := s.toolCalls[fragment.Index]
			if call == nil {
				call = &toolCallFragments{}
				s.toolCalls[fragment.Index] = call
			}
			if fragment.ID != "" {
				if call.id != "" && call.id != fragment.ID {
					return fmt.Errorf("tool call %d changed id", fragment.Index)
				}
				call.id = fragment.ID
			}
			if fragment.Type != "" {
				if fragment.Type != "function" {
					return fmt.Errorf("unsupported tool call type %q", fragment.Type)
				}
				call.kind = fragment.Type
			}
			call.name.WriteString(fragment.Function.Name)
			call.arguments.WriteString(fragment.Function.Arguments)
		}
		s.finishReason = choice.FinishReason
		if delta.Content != "" && onDelta != nil {
			onDelta(delta.Content)
		}
	}
	return nil
}

func (s *completionStream) result() (agent.ModelClientResult, error) {
	if s.finishReason == "" {
		return agent.ModelClientResult{}, fmt.Errorf(
			"chat completion stream missing finish reason: %w",
			io.ErrUnexpectedEOF,
		)
	}
	indexes := make([]int64, 0, len(s.toolCalls))
	for index := range s.toolCalls {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	calls := make([]*streamToolCall, 0, len(indexes))
	for i, index := range indexes {
		call := s.toolCalls[index]
		if index != int64(i) || call.id == "" || call.name.Len() == 0 || call.kind == "" {
			return agent.ModelClientResult{}, fmt.Errorf(
				"chat completion stream incomplete tool call at index %d",
				index,
			)
		}
		assembled := &streamToolCall{ID: call.id, Type: call.kind}
		assembled.Function.Name = call.name.String()
		assembled.Function.Arguments = call.arguments.String()
		calls = append(calls, assembled)
	}
	// Decode the assembled wire message through the same parser as batch mode,
	// including refusal fallback and opaque reasoning replay state.
	raw, err := json.Marshal(struct {
		Content          string            `json:"content"`
		Refusal          string            `json:"refusal"`
		ReasoningContent string            `json:"reasoning_content"`
		ReasoningOpaque  string            `json:"reasoning_opaque"`
		ToolCalls        []*streamToolCall `json:"tool_calls"`
	}{s.content.String(), s.refusal.String(), s.reasoningContent.String(), s.reasoningOpaque.String(), calls})
	if err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("marshal streamed message: %w", err)
	}
	var message openai.ChatCompletionMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return agent.ModelClientResult{}, fmt.Errorf("decode streamed message: %w", err)
	}
	messages, err := parseAssistantMessage(raw, message.ToolCalls)
	if err != nil {
		return agent.ModelClientResult{}, err
	}
	return agent.ModelClientResult{
		Messages:     messages,
		FinishReason: s.finishReason,
		Usage:        s.usage,
	}, nil
}
