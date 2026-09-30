package memory

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

const (
	// defaultListTurnsLimit is the page size used when a caller does not ask
	// for a specific limit.
	defaultListTurnsLimit = 50
	// maxListTurnsLimit is the largest page size accepted. Larger requests are
	// capped rather than rejected, so one client cannot pull the whole
	// transcript a page at a time with an absurd limit.
	maxListTurnsLimit = 500
)

// Turn is one durable transcript turn as a reader sees it.
type Turn struct {
	Seq       int64
	CreatedAt time.Time
	Messages  []conversation.Message
}

// TurnPage is one page of durable transcript turns, newest first.
type TurnPage struct {
	Turns   []Turn
	HeadSeq int64
	HasMore bool
}

// ListTurns pages the durable transcript, newest first. A caller pages
// backwards by passing the oldest seq it already holds; afterSeq lower bound
// is exclusive, so only turns strictly older than it come back, and zero
// starts at the newest turn. limit selects defaultListTurnsLimit when it is
// not positive and is capped at maxListTurnsLimit.
//
// This method is read-only: it opens turn records and the head state, never
// creating files or committing, and never calls syncHeadStateWithHistory.
// HeadSeq is the persisted head last_seq returned as-is. It may sit above the
// newest turn record, because a run reserves its sequence before the turn
// finishes, and turn files above head are ignored and never parsed for the
// same reason: a reader only ever sees history the writer has already claimed.
// A missing turns directory, or a transcript with no turns, is an empty page
// and a nil error.
func (s *Store) ListTurns(ctx context.Context, afterSeq int64, limit int) (TurnPage, error) {
	_ = ctx

	if limit <= 0 {
		limit = defaultListTurnsLimit
	}
	if limit > maxListTurnsLimit {
		limit = maxListTurnsLimit
	}

	release := s.repository.Acquire()
	defer release()

	head, err := s.readHeadState()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return TurnPage{}, err
		}
		// An uninitialized memory root has no head state and no transcripts,
		// which reads as an empty page at the zero sequence.
		head = headState{}
	}

	entries, err := s.listTurnEntries()
	if err != nil {
		return TurnPage{}, err
	}
	// listTurnEntries sorts ascending. Durable history ends at head, so the
	// upper bound is the first entry above it, and afterSeq is exclusive.
	end := sort.Search(len(entries), func(i int) bool {
		return entries[i].Seq > head.LastSeq
	})
	if afterSeq > 0 {
		older := sort.Search(end, func(i int) bool {
			return entries[i].Seq >= afterSeq
		})
		if older < end {
			end = older
		}
	}

	start := end - limit
	page := entries[max(0, start):end]
	turns := make([]Turn, 0, len(page))
	for i := len(page) - 1; i >= 0; i-- {
		record, err := s.readTurn(page[i].Path)
		if err != nil {
			return TurnPage{}, err
		}
		turns = append(turns, Turn{
			Seq:       page[i].Seq,
			CreatedAt: record.CreatedAt,
			Messages:  record.Messages,
		})
	}

	return TurnPage{
		Turns:   turns,
		HeadSeq: head.LastSeq,
		HasMore: start > 0,
	}, nil
}
