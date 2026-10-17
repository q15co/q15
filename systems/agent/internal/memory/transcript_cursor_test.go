package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestPartialTranscriptCheckpointSurvivesRestartAndDrains(t *testing.T) {
	for _, semantic := range []bool{true, false} {
		t.Run(map[bool]string{true: "semantic", false: "working"}[semantic], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store := newTestStore(root, &fakeCommitter{})
			if err := store.Init(ctx); err != nil {
				t.Fatal(err)
			}
			text := strings.Repeat("Synthetic — **evidence** `token`  ", 12000)
			if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(text)}); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage("tail")}); err != nil {
				t.Fatal(err)
			}
			seq := int64(0)
			var cursor *cognition.TranscriptCursor
			var joined strings.Builder
			fragments := 0
			for {
				window, err := store.LoadTranscriptWindow(ctx, seq, 8, cursor)
				if err != nil {
					t.Fatal(err)
				}
				if cognition.TranscriptArtifactBytes(
					window.Messages,
				) > cognition.MaxTranscriptWindowBytes {
					t.Fatal("window exceeded encoded budget")
				}
				for _, m := range window.Messages {
					for _, p := range m.Parts {
						joined.WriteString(p.Text)
					}
				}
				if window.PartialTurn != nil &&
					(window.LastSeq != 0 || window.LastIncludedSeq != 1) {
					t.Fatal("partial turn was marked complete")
				}
				seq, cursor = window.LastSeq, window.PartialTurn
				if semantic {
					if _, err := store.StoreSemanticExtractionCheckpoint(ctx, cognition.SemanticExtractionCheckpoint{LastExtractedSeq: seq, PartialTurn: cursor}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := store.StoreConsolidationCheckpoint(ctx, cognition.ConsolidationCheckpoint{LastConsolidatedSeq: seq, PartialTurn: cursor}); err != nil {
						t.Fatal(err)
					}
				}
				store = newTestStore(root, &fakeCommitter{})
				if err := store.Init(ctx); err != nil {
					t.Fatal(err)
				}
				if semantic {
					cp, err := store.LoadSemanticExtractionCheckpoint(ctx)
					if err != nil {
						t.Fatal(err)
					}
					seq, cursor = cp.LastExtractedSeq, cp.PartialTurn
				} else {
					cp, err := store.LoadConsolidationCheckpoint(ctx)
					if err != nil {
						t.Fatal(err)
					}
					seq, cursor = cp.LastConsolidatedSeq, cp.PartialTurn
				}
				fragments++
				if seq == 2 && cursor == nil {
					break
				}
				if fragments > 30 {
					t.Fatal("partial checkpoint did not drain")
				}
			}
			if fragments < 3 || joined.String() != text+"tail" {
				t.Fatalf(
					"restart lost or duplicated input: fragments=%d bytes=%d",
					fragments,
					joined.Len(),
				)
			}
		})
	}
}

func TestPartialTranscriptRefusesChangedInputAndSkippedTurns(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(strings.Repeat("synthetic ", 20000))}); err != nil {
		t.Fatal(err)
	}
	window, err := store.LoadTranscriptWindow(ctx, 0, 8, nil)
	if err != nil || window.PartialTurn == nil {
		t.Fatalf("partial window: %v", err)
	}
	bad := *window.PartialTurn
	bad.TurnSeq++
	if _, err := store.LoadTranscriptWindow(ctx, 0, 8, &bad); err == nil {
		t.Fatal("cursor skipped the first pending turn")
	}
	bad = *window.PartialTurn
	bad.InputHash = "different synthetic digest"
	if _, err := store.LoadTranscriptWindow(ctx, 0, 8, &bad); err == nil {
		t.Fatal("changed input became the cursor baseline")
	}
	entries, err := store.listTurnEntries()
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.readTurn(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	turn.Messages[0].Parts[0].Text += "changed synthetic evidence"
	if err := writeJSONFileAtomic(entries[0].Path, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTranscriptWindow(ctx, 0, 8, window.PartialTurn); err == nil {
		t.Fatal("changed source file was accepted at a saved byte position")
	}
}

func TestInitialPartialTranscriptResumesWithinSelectedTail(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t.TempDir(), &fakeCommitter{})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("synthetic selected evidence ", 6000)
	for _, value := range []string{"outside selected tail", text, "tail"} {
		if err := store.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(value)}); err != nil {
			t.Fatal(err)
		}
	}
	window, err := store.LoadLatestTranscriptWindow(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if window.LastSeq != 1 || window.PartialTurn == nil || window.PartialTurn.TurnSeq != 2 {
		t.Fatalf("initial partial window lost selected-tail boundary: %#v", window)
	}
	var joined strings.Builder
	for fragments := 0; ; fragments++ {
		if fragments > 10 {
			t.Fatal("selected tail did not drain")
		}
		for _, message := range window.Messages {
			joined.WriteString(conversation.TextValue(message))
		}
		if window.PartialTurn == nil && window.RemainingTurns == 0 {
			break
		}
		window, err = store.LoadTranscriptWindow(ctx, window.LastSeq, 2, window.PartialTurn)
		if err != nil {
			t.Fatal(err)
		}
	}
	if window.LastSeq != 3 || joined.String() != text+"tail" {
		t.Fatal("initial tail repeated old evidence or skipped its partial turn")
	}
}
