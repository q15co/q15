package memory

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

var _ cognition.InitialTranscriptWindowLoader = (*Store)(nil)

// LoadLatestTranscriptWindow selects the same latest tail as initial cognition
// replay, but returns a byte-bounded prefix with complete and partial boundaries.
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
	base := int64(0)
	if start > 0 {
		base = entries[start-1].Seq
	}
	return s.loadByteBoundedTranscriptWindowLocked(entries[start:], maxTurns, base, nil)
}

// LoadTranscriptWindow resumes at the oldest pending turn, including any saved
// partial position, without advancing past unprocessed bytes.
func (s *Store) LoadTranscriptWindow(
	ctx context.Context,
	afterSeq int64,
	maxTurns int,
	cursor *cognition.TranscriptCursor,
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
	start := sort.Search(len(entries), func(i int) bool { return entries[i].Seq > afterSeq })
	return s.loadByteBoundedTranscriptWindowLocked(entries[start:], maxTurns, afterSeq, cursor)
}

func (s *Store) loadByteBoundedTranscriptWindowLocked(
	entries []turnPathEntry,
	maxTurns int,
	afterSeq int64,
	cursor *cognition.TranscriptCursor,
) (cognition.TurnWindow, error) {
	if len(entries) == 0 {
		if cursor != nil {
			return cognition.TurnWindow{}, fmt.Errorf("partial transcript turn is missing")
		}
		return cognition.TurnWindow{}, nil
	}
	if cursor != nil && (cursor.TurnSeq != entries[0].Seq || cursor.TurnSeq <= afterSeq) {
		return cognition.TurnWindow{}, fmt.Errorf(
			"partial transcript cursor would skip pending turns",
		)
	}
	window := cognition.TurnWindow{LastSeq: afterSeq, RemainingTurns: len(entries)}
	for _, entry := range entries[:min(maxTurns, len(entries))] {
		turn, err := s.readTurn(entry.Path)
		if err != nil {
			return cognition.TurnWindow{}, err
		}
		messages := promptVisibleMessages(turn.Messages)
		candidate := append(window.Messages, messages...)
		if cursor != nil ||
			cognition.TranscriptArtifactBytes(candidate) > cognition.MaxTranscriptWindowBytes {
			if len(window.Messages) > 0 {
				break
			}
			raw, err := json.Marshal(messages)
			if err != nil {
				return cognition.TurnWindow{}, err
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(raw))
			if cursor != nil && cursor.InputHash != hash {
				return cognition.TurnWindow{}, fmt.Errorf("partial transcript input changed")
			}
			fragment, next, err := cognition.CutTranscriptTurn(
				messages,
				cursor,
				cognition.MaxTranscriptWindowBytes,
			)
			if err != nil {
				return cognition.TurnWindow{}, err
			}
			window.Messages, window.LastIncludedSeq = fragment, entry.Seq
			window.RemainingTurns--
			if next != nil {
				next.TurnSeq, next.InputHash = entry.Seq, hash
				window.PartialTurn = next
			} else {
				window.LastSeq = entry.Seq
			}
			return window, nil
		}
		window.Messages = candidate
		window.LastSeq, window.LastIncludedSeq = entry.Seq, entry.Seq
		window.RemainingTurns--
	}
	return window, nil
}
