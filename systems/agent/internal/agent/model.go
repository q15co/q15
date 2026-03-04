package agent

import (
	"context"
	"encoding/json"
)

// Role identifies the speaker role for a chat message.
type Role string

const (
	// SystemRole is a system instruction message.
	SystemRole Role = "system"
	// UserRole is an end-user message.
	UserRole Role = "user"
	// AssistantRole is a model assistant message.
	AssistantRole Role = "assistant"
	// ToolRole is a synthetic message containing tool output.
	ToolRole Role = "tool"
)

// Message is a model-facing chat message used by the loop.
type Message struct {
	// Role is the message author role.
	Role Role
	// Content is the text payload for this message.
	Content string
	// ToolCalls are requested tool invocations emitted by the model.
	ToolCalls []ToolCall
	// ToolCallID links a tool result message back to its originating call.
	ToolCallID string
	// ProviderRaw stores an optional provider-specific raw assistant message payload.
	// The core loop treats this as opaque pass-through data.
	ProviderRaw json.RawMessage
}

// ToolCall describes one requested tool invocation.
type ToolCall struct {
	// ID uniquely identifies the call within a model response.
	ID string
	// Name is the registered tool name.
	Name string
	// Arguments is the raw JSON argument object.
	Arguments string
}

// ToolDefinition describes a callable tool exposed to the model.
type ToolDefinition struct {
	// Name is the tool identifier referenced in tool calls.
	Name string
	// Description explains what the tool does.
	Description string
	// Parameters is the JSON-schema-like parameter definition.
	Parameters map[string]any
}

// ModelClientResult is the output of one model completion call.
type ModelClientResult struct {
	// Content is the assistant text returned by the model.
	Content string
	// ToolCalls are requested tool invocations returned by the model.
	ToolCalls []ToolCall
	// FinishReason is the provider-reported completion reason when available.
	FinishReason string
	// ProviderRaw is the raw assistant message payload returned by the model adapter.
	ProviderRaw json.RawMessage
}

// ModelClient adapts a model provider to the loop.
type ModelClient interface {
	// Complete runs one completion for the selected model using message history and
	// optional tool definitions.
	Complete(
		ctx context.Context,
		model string,
		messages []Message,
		tools []ToolDefinition,
	) (ModelClientResult, error)
}

// Tool is a runnable capability exposed to the model.
type Tool interface {
	// Definition returns the static tool metadata exposed to the model.
	Definition() ToolDefinition
	// Run executes the tool with raw JSON arguments.
	Run(ctx context.Context, arguments string) (string, error)
}

// ToolRegistry resolves and executes tools requested by the model.
type ToolRegistry interface {
	// Definitions returns all tool definitions visible to the model.
	Definitions() []ToolDefinition
	// Run executes a single tool call.
	Run(ctx context.Context, call ToolCall) (string, error)
}
