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

// InferRequirements derives the model capabilities required for one request.
//
// Only text is a hard requirement. Media (image/audio) is handled by adaptive
// rendering at the provider boundary — a text-only model receives media as text
// hints instead of being excluded from selection.
//
// Tool calling is not auto-required yet because existing behavior allows
// fallback to eligible non-tool models by omitting tools.
func InferRequirements(_ Request) Requirements {
	return Requirements{Text: true}
}
