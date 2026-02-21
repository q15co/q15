package agent

import (
	"context"
	"encoding/json"
)

type Role string

const (
	SystemRole    Role = "system"
	UserRole      Role = "user"
	AssistantRole Role = "assistant"
	ToolRole      Role = "tool"
)

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// ProviderRaw stores an optional provider-specific raw assistant message payload.
	// The core loop treats this as opaque pass-through data.
	ProviderRaw json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ModelResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	// ProviderRaw is the raw assistant message payload returned by the model adapter.
	ProviderRaw json.RawMessage
}

type Model interface {
	Complete(ctx context.Context, model string, messages []Message, tools []ToolDefinition) (ModelResult, error)
}

type ToolRunner interface {
	Definitions() []ToolDefinition
	Run(ctx context.Context, call ToolCall) (string, error)
}
