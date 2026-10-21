package cognition

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestTranscriptCursorCoversEveryUTF8ByteAcrossParts(t *testing.T) {
	text := strings.Repeat("**Synthetic** — `token`  ", 3000)
	reasoning := strings.Repeat("reasoning (synthetic) \x01 ", 1000)
	messages := []conversation.Message{
		conversation.UserMessage(text),
		conversation.AssistantMessage(conversation.Reasoning(reasoning, nil)),
		conversation.UserMessage("final evidence"),
	}
	var cursor *TranscriptCursor
	var joined strings.Builder
	fragments := 0
	for {
		fragment, next, err := CutTranscriptTurn(messages, cursor, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if TranscriptArtifactBytes(fragment) > 4096 {
			t.Fatal("fragment exceeded encoded budget")
		}
		for _, message := range fragment {
			for _, part := range message.Parts {
				joined.WriteString(part.Text)
			}
		}
		fragments++
		if next == nil {
			break
		}
		if cursor != nil &&
			(next.Message < cursor.Message || (next.Message == cursor.Message && next.Part < cursor.Part) || (next.Message == cursor.Message && next.Part == cursor.Part && next.ByteOffset <= cursor.ByteOffset)) {
			t.Fatalf("cursor did not progress: %v -> %v", cursor, next)
		}
		cursor = next
		if fragments > 200 {
			t.Fatal("cursor did not terminate")
		}
	}
	if fragments < 2 || joined.String() != text+reasoning+"final evidence" {
		t.Fatalf(
			"fragmentation omitted or repeated evidence: fragments=%d bytes=%d",
			fragments,
			joined.Len(),
		)
	}
}

func TestTranscriptCursorRejectsInvalidPositions(t *testing.T) {
	messages := []conversation.Message{conversation.UserMessage("— synthetic")}
	for _, cursor := range []*TranscriptCursor{{Message: -1}, {Part: 2}, {ByteOffset: 1}, {ByteOffset: 1000}, {ByteOffset: -1}} {
		if _, _, err := CutTranscriptTurn(messages, cursor, 4096); err == nil {
			t.Fatalf("accepted invalid cursor: %#v", cursor)
		}
	}
}

func TestControllerPartialTranscriptDoesNotAdvancePastFailedFragment(t *testing.T) {
	for _, jobType := range []string{semanticMemoryExtractionJobType, workingMemoryConsolidationJobType} {
		t.Run(jobType, func(t *testing.T) {
			ctx := context.Background()
			store := newFakeControllerStore()
			store.setHead(1, time.Now().UTC())
			progress := &transcriptProgress{
				PartialTurn: &TranscriptCursor{TurnSeq: 1, ByteOffset: 123, InputHash: "synthetic"},
			}
			fail := false
			c := newControllerForTest(t, store, JobRegistration{NewJob: func() JobDefinition {
				return fakeJob{
					jobType: jobType,
					build: func(context.Context, ContextLoader) (Spec, error) {
						return Spec{
							Objective:          "Process synthetic fragment.",
							CompletionContract: "Return a result.",
							CheckpointSeq:      1,
							transcriptProgress: progress,
						}, nil
					},
					apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
						if fail {
							return ParsedResult{}, errors.New("synthetic fragment failure")
						}
						return ParsedResult{CheckpointSeq: 1}, nil
					},
				}
			}, Policy: TriggerPolicy{State: []StateRule{{ID: "threshold_not_met", Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) { return false, "", nil }}}}})
			pending := pendingRun{job: c.jobs[0], headSeq: 1, headAt: time.Now().UTC()}
			if err := c.runPending(ctx, pending); err != nil {
				t.Fatal(err)
			}
			checkpoint := func() (int64, *TranscriptCursor) {
				if jobType == semanticMemoryExtractionJobType {
					cp := store.semanticCheckpoint()
					return cp.LastExtractedSeq, cp.PartialTurn
				}
				cp := store.consolidationCheckpoint()
				return cp.LastConsolidatedSeq, cp.PartialTurn
			}
			if seq, cursor := checkpoint(); seq != 0 || cursor == nil || cursor.ByteOffset != 123 {
				t.Fatalf("partial turn marked complete: seq=%d cursor=%v", seq, cursor)
			}
			if state := store.state(jobType); state.LastRunInputSeq != 0 {
				t.Fatalf("partial turn advanced input beyond covered work: %#v", state)
			}
			next, ok, err := c.nextPendingRun(ctx, false, false, true)
			if err != nil || !ok {
				t.Fatalf("partial continuation waited for a new turn: %v %v", ok, err)
			}
			progress = &transcriptProgress{
				PartialTurn: &TranscriptCursor{TurnSeq: 1, ByteOffset: 246, InputHash: "synthetic"},
			}
			fail = true
			if err := c.runPending(ctx, next); err != nil {
				t.Fatal(err)
			}
			if seq, cursor := checkpoint(); seq != 0 || cursor == nil || cursor.ByteOffset != 123 {
				t.Fatal("failed fragment advanced its durable cursor")
			}
			// #379: the failed fragment armed a backoff window; move past it.
			expireFailureBackoff(t, store, jobType)
			next, ok, err = c.nextPendingRun(ctx, false, false, true)
			if err != nil || !ok || next.cause.RuleID != "transcript_continuation" {
				t.Fatalf("failed fragment lost continuation: %v %v", ok, err)
			}
			fail = false
			progress = &transcriptProgress{CompletedSeq: 1}
			if err := c.runPending(ctx, next); err != nil {
				t.Fatal(err)
			}
			if seq, cursor := checkpoint(); seq != 1 || cursor != nil {
				t.Fatal("completed turn did not clear the partial cursor")
			}
		})
	}
}
