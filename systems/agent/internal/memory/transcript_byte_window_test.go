package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestCognitionWindowStopsAtRenderedByteBoundary(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"one", "two", "three"} {
		if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(marker + strings.Repeat("x", 24*1024))}); err != nil {
			t.Fatal(err)
		}
	}
	window, err := store.LoadMessagesSinceSeqWindow(ctx, 0, 8)
	if err != nil || window.LastSeq != 2 || window.RemainingTurns != 1 ||
		len(window.Messages) != 2 {
		t.Fatalf(
			"byte window: last=%d remaining=%d messages=%d error=%v",
			window.LastSeq,
			window.RemainingTurns,
			len(window.Messages),
			err,
		)
	}
	window, err = store.LoadMessagesSinceSeqWindow(ctx, window.LastSeq, 8)
	if err != nil || window.LastSeq != 3 || window.RemainingTurns != 0 ||
		len(window.Messages) != 1 ||
		!strings.HasPrefix(conversation.TextValue(window.Messages[0]), "three") {
		t.Fatalf("byte window skipped tail: %#v %v", window, err)
	}
}

func TestCognitionWindowDoesNotSkipOversizedSingleTurn(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", strings.Repeat("x", 128*1024), "third"} {
		if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(text)}); err != nil {
			t.Fatal(err)
		}
	}
	window, err := store.LoadMessagesSinceSeqWindow(ctx, 0, 8)
	if err != nil || window.LastSeq != 1 || window.RemainingTurns != 2 {
		t.Fatalf(
			"window crossed oversized turn: last=%d remaining=%d error=%v",
			window.LastSeq,
			window.RemainingTurns,
			err,
		)
	}
	blocked, err := store.LoadMessagesSinceSeqWindow(ctx, window.LastSeq, 8)
	if err != nil || blocked.LastSeq != 1 || blocked.PartialTurn == nil ||
		blocked.PartialTurn.TurnSeq != 2 ||
		len(blocked.Messages) == 0 {
		t.Fatalf(
			"oversized turn was skipped instead of partially covered: last=%d error=%v",
			blocked.LastSeq,
			err,
		)
	}
	recent, err := store.LoadLatestMessages(ctx, 2)
	if err != nil || len(recent) != 2 || len(conversation.TextValue(recent[0])) != 128*1024 {
		t.Fatal("interactive latest-turn behavior changed")
	}
}

func TestInitialCognitionWindowKeepsSelectedTailAndReportsCoveredPrefix(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"outside-selected-tail", "one", "two", "three"} {
		if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(marker + strings.Repeat("x", 24*1024))}); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := store.LoadLatestTranscriptWindow(ctx, 3)
	if err != nil || initial.LastSeq != 3 || initial.RemainingTurns != 1 ||
		len(initial.Messages) != 2 ||
		!strings.HasPrefix(conversation.TextValue(initial.Messages[0]), "one") {
		t.Fatalf(
			"initial tail window: last=%d remaining=%d messages=%d error=%v",
			initial.LastSeq,
			initial.RemainingTurns,
			len(initial.Messages),
			err,
		)
	}
	next, err := store.LoadMessagesSinceSeqWindow(ctx, initial.LastSeq, 3)
	if err != nil || next.LastSeq != 4 || next.RemainingTurns != 0 ||
		!strings.HasPrefix(conversation.TextValue(next.Messages[0]), "three") {
		t.Fatalf("initial window skipped tail: last=%d error=%v", next.LastSeq, err)
	}
}

func TestCognitionWindowCountsJSONEscaping(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(strings.Repeat("\x01", 12*1024))}); err != nil {
		t.Fatal(err)
	}
	window, err := store.LoadMessagesSinceSeqWindow(ctx, 0, 1)
	if err != nil || window.LastSeq != 0 || window.PartialTurn == nil ||
		len(conversation.TextValue(window.Messages[0])) >= 12*1024 {
		t.Fatal("window measured raw text instead of serialized evidence")
	}
}
