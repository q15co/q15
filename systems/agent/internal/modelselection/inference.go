package modelselection

import "github.com/q15co/q15/systems/agent/internal/conversation"

// Request captures the canonical inputs needed to infer model requirements for
// one turn.
type Request struct {
	Messages []conversation.Message
	// ToolCount is reserved for future explicit tool-capability inference. The
	// presence of tools does not imply ToolCalling today because q15 still
	// allows fallback to eligible non-tool models by omitting tools.
	ToolCount int
}

// InferRequirements derives the model capabilities required for one request
// from the canonical transcript and the currently visible tool set.
//
// Tool calling is not auto-required yet because existing behavior allows
// fallback to eligible non-tool models by omitting tools.
func InferRequirements(request Request) Requirements {
	_ = request.ToolCount

	requirements := Requirements{
		Text: true,
	}
	if conversation.HasImageParts(request.Messages) {
		requirements.ImageInput = true
	}
	return requirements
}
