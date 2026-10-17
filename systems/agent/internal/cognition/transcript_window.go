package cognition

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// MaxTranscriptWindowBytes bounds JSON-encoded transcript evidence and framing.
const MaxTranscriptWindowBytes = 64 * 1024

// InitialTranscriptWindowLoader preserves sequence boundaries when a job's
// initial latest-turn replay has to stop before the selected tail ends.
type InitialTranscriptWindowLoader interface {
	LoadLatestTranscriptWindow(context.Context, int) (TurnWindow, error)
}

// TranscriptArtifactBytes includes JSON escaping of the rendered artifact,
// rather than measuring only raw text or counting turns.
func TranscriptArtifactBytes(messages []conversation.Message) int {
	encoded, _ := json.Marshal(renderTranscriptArtifact(messages))
	return len(encoded)
}

func loadInitialTranscriptWindow(
	ctx context.Context,
	loader ContextLoader,
	turns int,
	fallback func(context.Context, int) ([]conversation.Message, error),
) (TurnWindow, error) {
	if windowed, ok := loader.(InitialTranscriptWindowLoader); ok {
		return windowed.LoadLatestTranscriptWindow(ctx, turns)
	}
	messages, err := fallback(ctx, turns)
	if err != nil {
		return TurnWindow{}, err
	}
	if size := TranscriptArtifactBytes(messages); size > MaxTranscriptWindowBytes {
		return TurnWindow{}, fmt.Errorf(
			"initial transcript exceeds byte budget (%d>%d); loader must preserve turn sequence boundaries to split it",
			size,
			MaxTranscriptWindowBytes,
		)
	}
	return TurnWindow{Messages: messages}, nil
}

func renderInitialTranscriptScope(policy string, turns int, window TurnWindow) string {
	scope := renderTranscriptScopeWithPolicy(policy, turns, len(window.Messages))
	if window.RemainingTurns > 0 {
		scope += fmt.Sprintf(
			"\n%d later turns in the selected tail remain unprocessed and will be covered by later runs.",
			window.RemainingTurns,
		)
	}
	return scope
}
