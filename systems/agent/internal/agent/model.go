package agent

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

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
	// PromptGuidance adds concise model-facing usage rules for prompt
	// composition.
	PromptGuidance []string
	// Parameters is the JSON-schema-like parameter definition.
	Parameters map[string]any
}

// ModelClientResult is the output of one model completion call.
type ModelClientResult struct {
	// Messages are the ordered canonical conversation.Message items returned by
	// the model. This is the only transcript shape providers return to the loop.
	Messages []conversation.Message
	// FinishReason is the provider-reported completion reason when available.
	FinishReason string
}

// ModelClient adapts a model provider to the loop using canonical
// conversation.Message history. Provider-native request/response details stay
// inside the adapter.
type ModelClient interface {
	// Complete runs one completion for the selected model using message history and
	// optional tool definitions.
	Complete(
		ctx context.Context,
		model string,
		messages []conversation.Message,
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
