package modelselection

import "github.com/q15co/q15/systems/agent/internal/conversation"

// Request captures the canonical inputs needed to infer model requirements for
// one turn.
type Request struct {
	Messages  []conversation.Message
	ToolCount int
}

// InferRequirements derives the model capabilities required for one request
// from the canonical transcript and the currently visible tool set.
//
// Today q15 only infers the one requirement it can prove for every model turn:
// text. Tool calling is not auto-required yet because existing behavior allows
// fallback to eligible non-tool models by omitting tools. Image input will be
// inferred once canonical transcript turns can carry image parts.
func InferRequirements(request Request) Requirements {
	_ = request.Messages
	_ = request.ToolCount

	return Requirements{
		Text: true,
	}
}
