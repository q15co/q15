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

// ToolResult is the structured result of one tool invocation.
type ToolResult struct {
	// Output is the textual result returned to the model as the tool-result body.
	Output string
	// Attachments are typed transcript parts attached to the tool result for
	// follow-up multimodal inspection on the next model turn.
	Attachments []conversation.Part
	// MediaRefs is the legacy image-ref convenience field. When Attachments is
	// empty, these refs are treated as image attachments.
	MediaRefs []string
}

// ReplyResult is the structured end-user response for one completed turn.
type ReplyResult struct {
	// Text is the final assistant text to render back to the user.
	Text string
	// Attachments are typed canonical transcript parts attached to the final
	// assistant response. Transports may support only a subset of attachment
	// types.
	Attachments []conversation.Part
	// MediaRefs is the legacy image-ref convenience field derived from
	// Attachments when possible.
	MediaRefs []string
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

// StructuredTool is an optional extension for tools that can return media refs
// or other structured result metadata in addition to text output.
type StructuredTool interface {
	RunResult(ctx context.Context, arguments string) (ToolResult, error)
}

// ToolRegistry resolves and executes tools requested by the model.
type ToolRegistry interface {
	// Definitions returns all tool definitions visible to the model.
	Definitions() []ToolDefinition
	// Run executes a single tool call.
	Run(ctx context.Context, call ToolCall) (ToolResult, error)
}
