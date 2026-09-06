package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestDraftOverflowDuringInitialSendStillPersistsNewFinalMessage(t *testing.T) {
	f := &fakeDraftChannel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	s.showStatus(ctx, thinkingStatus)
	draftDelta(ctx, s, "Partial")
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("initial draft did not start")
	}
	draftDelta(ctx, s, strings.Repeat("x", telegramDraftMaxBytes))
	close(f.release)
	s.Finish(ctx, agent.ReplyResult{Text: "Complete answer"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendTexts) != 1 || f.sendTexts[0] != "Complete answer" {
		t.Fatalf(
			"overflow final must clear the draft with a new message: sends=%v edits=%v",
			f.sendTexts,
			f.editTexts,
		)
	}
	for _, edit := range f.editTexts {
		if edit == "Complete answer" {
			t.Fatal("editing an old placeholder leaves the draft visible")
		}
	}
}

func TestDraftResetDuringInitialSendReplacesStaleAttemptPromptly(t *testing.T) {
	fastDraftTimings(t)
	telegramDraftKeepaliveInterval = time.Second
	f := &fakeDraftChannel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	draftDelta(ctx, s, "Failed attempt")
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("initial draft did not start")
	}
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnStarted, ModelRef: "fallback"})
	close(f.release)
	waitForCondition(t, 500*time.Millisecond, func() bool { return len(f.snapshotDrafts()) >= 2 })
	if got := f.snapshotDrafts()[1].text; got != thinkingStatus {
		t.Fatalf("stale attempt replacement = %q", got)
	}
	s.Finish(ctx, agent.ReplyResult{Text: "Final"})
}

func TestDraftRetriesFailedPlaceholderDeletionAtCompletion(t *testing.T) {
	f := &fakeDraftChannel{}
	f.deleteErr = errors.New("temporary deletion failure")
	ctx := context.Background()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	s.showStatus(ctx, thinkingStatus)
	draftDelta(ctx, s, "Partial")
	waitForCondition(t, time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.draft.orphanStatusID != ""
	})
	f.mu.Lock()
	f.deleteErr = nil
	f.mu.Unlock()
	s.Finish(ctx, agent.ReplyResult{Text: "Final"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deletedMessages) != 2 || f.deletedMessages[0] != f.deletedMessages[1] ||
		len(f.sendTexts) != 1 {
		t.Fatalf("placeholder cleanup attempts=%v final=%v", f.deletedMessages, f.sendTexts)
	}
}

func TestStopWinsWhileFinalWaitsForDraftRequest(t *testing.T) {
	f := &fakeDraftChannel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	s.SetCancel(cancel)
	s.showStatus(ctx, thinkingStatus)
	draftDelta(ctx, s, "Partial")
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal("draft request did not start")
	}
	done := make(chan struct{})
	go func() {
		s.Finish(ctx, agent.ReplyResult{Text: "must not send"})
		close(done)
	}()
	f.stops.handleStoppedMessageGeneration(telego.MessageGenerationStopped{
		Chat: telego.Chat{ID: 123, Type: telego.ChatTypePrivate}, DraftID: s.draft.id,
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stopped finalization did not clean up")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendTexts) != 0 || len(f.editTexts) != 1 ||
		f.editTexts[0] != stoppedStatus("canceled") {
		t.Fatalf("Stop/final race: sends=%v edits=%v", f.sendTexts, f.editTexts)
	}
}

func TestCapturedStopCannotCancelCompletedSession(t *testing.T) {
	f := &fakeDraftChannel{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	s.SetCancel(cancel)
	f.stops.draftMu.Lock()
	stop := f.stops.draftStops[draftKey{chatID: 123, draftID: s.draft.id}]
	f.stops.draftMu.Unlock()
	if stop == nil {
		t.Fatal("missing registered Stop callback")
	}
	s.Finish(ctx, agent.ReplyResult{Text: "Final"})
	stop()
	if ctx.Err() != nil {
		t.Fatal("late captured Stop callback canceled an already completed session")
	}
}
