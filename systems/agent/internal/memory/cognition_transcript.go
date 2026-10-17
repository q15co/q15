package memory

import (
	"context"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

var _ cognition.InitialTranscriptWindowLoader = (*Store)(nil)

// LoadLatestTranscriptWindow selects the same latest tail as initial cognition
// replay, but returns its oldest complete byte-bounded prefix and real cursor.
func (s *Store) LoadLatestTranscriptWindow(
	ctx context.Context,
	maxTurns int,
) (cognition.TurnWindow, error) {
	if err := ctx.Err(); err != nil {
		return cognition.TurnWindow{}, err
	}
	if maxTurns <= 0 {
		return cognition.TurnWindow{}, nil
	}
	release := s.repository.Acquire()
	defer release()
	entries, err := s.listTurnEntries()
	if err != nil {
		return cognition.TurnWindow{}, err
	}
	start := max(0, len(entries)-maxTurns)
	return s.loadByteBoundedTranscriptWindowLocked(entries[start:], maxTurns)
}

func (s *Store) loadByteBoundedTranscriptWindowLocked(
	entries []turnPathEntry,
	maxTurns int,
) (cognition.TurnWindow, error) {
	window := cognition.TurnWindow{RemainingTurns: len(entries)}
	for _, entry := range entries[:min(maxTurns, len(entries))] {
		turn, err := s.readTurn(entry.Path)
		if err != nil {
			return cognition.TurnWindow{}, err
		}
		candidate := append(window.Messages, promptVisibleMessages(turn.Messages)...)
		if size := cognition.TranscriptArtifactBytes(candidate); size > cognition.MaxTranscriptWindowBytes {
			if window.LastSeq == 0 {
				return cognition.TurnWindow{}, fmt.Errorf(
					"transcript turn seq %d exceeds cognition byte budget (%d>%d); no evidence was skipped",
					entry.Seq,
					size,
					cognition.MaxTranscriptWindowBytes,
				)
			}
			break
		}
		window.Messages = candidate
		window.LastSeq = entry.Seq
		window.RemainingTurns--
	}
	return window, nil
}
