package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

type sentDraft struct {
	id      int
	text    string
	canStop bool
	at      time.Time
}

type fakeDraftChannel struct {
	fakeAgentRunChannel
	draftMu  sync.Mutex
	drafts   []sentDraft
	draftErr error
	stops    Channel
	entered  chan struct{}
	release  chan struct{}
}

func (f *fakeDraftChannel) SendTextDraft(
	ctx context.Context,
	_ string,
	id int,
	text string,
	canStop bool,
) error {
	f.draftMu.Lock()
	f.drafts = append(f.drafts, sentDraft{id: id, text: text, canStop: canStop, at: time.Now()})
	err := f.draftErr
	f.draftMu.Unlock()
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.release:
		}
	}
	return err
}

func (f *fakeDraftChannel) registerDraftStop(
	chatID string,
	id int,
	cancel context.CancelFunc,
) func() {
	return f.stops.registerDraftStop(chatID, id, cancel)
}

func (f *fakeDraftChannel) snapshotDrafts() []sentDraft {
	f.draftMu.Lock()
	defer f.draftMu.Unlock()
	return append([]sentDraft(nil), f.drafts...)
}

func fastDraftTimings(t *testing.T) {
	t.Helper()
	update, keepalive := telegramDraftUpdateInterval, telegramDraftKeepaliveInterval
	telegramDraftUpdateInterval = 30 * time.Millisecond
	telegramDraftKeepaliveInterval = 100 * time.Millisecond
	t.Cleanup(func() {
		telegramDraftUpdateInterval, telegramDraftKeepaliveInterval = update, keepalive
	})
}

func draftDelta(ctx context.Context, s *agentRunSession, text string) {
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: text})
}

func TestDraftCoalescesKeepsAliveAndFinalizesOnce(t *testing.T) {
	fastDraftTimings(t)
	f := &fakeDraftChannel{}
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.SetCancel(cancel)
	s.showStatus(ctx, thinkingStatus)
	draftDelta(ctx, s, "Hello")
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) == 1 })
	for range 100 {
		draftDelta(ctx, s, "!")
	}
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) >= 2 })
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) >= 3 })
	drafts := f.snapshotDrafts()
	if len(drafts) != 3 || drafts[1].text != "Hello"+strings.Repeat("!", 100) ||
		drafts[2].text != drafts[1].text {
		t.Fatalf("drafts = %#v, want coalesced content followed by keepalive", drafts)
	}
	for i, draft := range drafts {
		if draft.id == 0 || draft.id != drafts[0].id || !draft.canStop {
			t.Fatalf("draft[%d] = %#v, want stable stoppable ID", i, draft)
		}
		if i > 0 && draft.at.Sub(drafts[i-1].at) < telegramDraftUpdateInterval-2*time.Millisecond {
			t.Fatalf("draft interval = %s, below throttle", draft.at.Sub(drafts[i-1].at))
		}
	}
	s.Finish(ctx, agent.ReplyResult{Text: "Complete answer"})
	s.Finish(ctx, agent.ReplyResult{Text: "duplicate"})
	s.Abort(ctx, "too late")
	draftDelta(ctx, s, "late delta")
	time.Sleep(2 * telegramDraftKeepaliveInterval)
	if got := len(f.snapshotDrafts()); got != len(drafts) {
		t.Fatalf("draft count after final = %d, want %d", got, len(drafts))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendTexts) != 1 || f.sendTexts[0] != "Complete answer" || len(f.editTexts) != 0 ||
		len(f.deletedMessages) != 1 {
		t.Fatalf(
			"final sends=%#v edits=%#v deleted=%#v",
			f.sendTexts,
			f.editTexts,
			f.deletedMessages,
		)
	}
}

func TestDraftModesAndNonstreamingFallback(t *testing.T) {
	for _, mode := range []progressMode{progressModeQuiet, progressModeProgress, progressModeVerbose} {
		t.Run(string(mode), func(t *testing.T) {
			f := &fakeDraftChannel{}
			s := newAgentRunSession(f, "123", "", mode)
			t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
			ctx := context.Background()
			if mode != progressModeQuiet {
				s.showStatus(ctx, thinkingStatus)
			}
			s.Finish(ctx, agent.ReplyResult{Text: "Nonstreaming answer"})
			if len(f.snapshotDrafts()) != 0 {
				t.Fatal("nonstreaming run emitted drafts")
			}
			if mode == progressModeQuiet {
				s = newAgentRunSession(f, "123", "", mode)
				draftDelta(ctx, s, "Hidden partial")
				s.Finish(ctx, agent.ReplyResult{Text: "Final"})
				if s.draft != nil || len(f.snapshotDrafts()) != 0 {
					t.Fatal("quiet run emitted a draft")
				}
			}
		})
	}
	if s := newAgentRunSession(&fakeDraftChannel{}, "-100123", "", progressModeProgress); s.draft != nil {
		t.Fatal("group chats must use progress fallback")
	}
}

func TestDraftFailureRestoresPlaceholderAndPreservesFinal(t *testing.T) {
	fastDraftTimings(t)
	f := &fakeDraftChannel{draftErr: errors.New("drafts unsupported")}
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx := context.Background()
	draftDelta(ctx, s, "Partial answer")
	waitForCondition(t, time.Second, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.sendMessageTexts) == 1
	})
	draftDelta(ctx, s, " more content")
	s.Finish(ctx, agent.ReplyResult{Text: "Complete answer"})
	if got := len(f.snapshotDrafts()); got != 1 {
		t.Fatalf("draft attempts = %d, want one before fallback", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.editTexts) != 1 || f.editTexts[0] != "Complete answer" || len(f.sendTexts) != 0 {
		t.Fatalf("final edits=%#v sends=%#v", f.editTexts, f.sendTexts)
	}
}

func TestDraftNewAttemptResetsFailedContentAndShowsToolActivity(t *testing.T) {
	fastDraftTimings(t)
	f := &fakeDraftChannel{}
	s := newAgentRunSession(f, "123", "", progressModeVerbose)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx := context.Background()
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "first"},
	)
	draftDelta(ctx, s, "Failed partial")
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) == 1 })
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "fallback"},
	)
	draftDelta(ctx, s, "Checking files")
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventToolStarted, ToolCall: agent.ToolCall{
		Name: "read_file", Arguments: `{"path":"/workspace/main.go"}`,
	}})
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) >= 2 })
	drafts := f.snapshotDrafts()
	if drafts[1].text != "Checking files\n\n📖 Reading `/workspace/main.go`" ||
		drafts[1].id != drafts[0].id {
		t.Fatalf("next draft = %#v", drafts[1])
	}
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnStarted, Turn: 2})
	draftDelta(ctx, s, "Answer after tools")
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) >= 3 })
	if got := f.snapshotDrafts()[2].text; got != "Answer after tools" {
		t.Fatalf("next model turn draft = %q", got)
	}
	s.Finish(ctx, agent.ReplyResult{Text: "Answer after tools"})
}

func TestDraftStopCancelsOnlyMatchingActiveRun(t *testing.T) {
	fastDraftTimings(t)
	f := &fakeDraftChannel{}
	f.stops.allowedUserIDs = map[int64]struct{}{123: {}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	s.SetCancel(cancel)
	draftDelta(ctx, s, "Partial")
	waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) == 1 })
	stop := telego.MessageGenerationStopped{
		Chat:    telego.Chat{ID: 123, Type: telego.ChatTypePrivate},
		DraftID: s.draft.id,
	}
	for _, invalid := range []telego.MessageGenerationStopped{
		{Chat: stop.Chat, DraftID: stop.DraftID + 1},
		{Chat: telego.Chat{ID: 456, Type: telego.ChatTypePrivate}, DraftID: stop.DraftID},
		{Chat: stop.Chat, DraftID: stop.DraftID, MessageThreadID: 1},
		{Chat: telego.Chat{ID: 123, Type: telego.ChatTypeGroup}, DraftID: stop.DraftID},
	} {
		f.stops.handleStoppedMessageGeneration(invalid)
		if ctx.Err() != nil {
			t.Fatalf("invalid stop canceled run: %#v", invalid)
		}
	}
	f.stops.handleStoppedMessageGeneration(stop)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("matching stop did not cancel run")
	}
	s.Abort(context.Background(), "canceled")
	s.Finish(context.Background(), agent.ReplyResult{Text: "must not send"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendTexts) != 1 || f.sendTexts[0] != stoppedStatus("canceled") {
		t.Fatalf("stop sends = %#v", f.sendTexts)
	}
	f.stops.draftMu.Lock()
	defer f.stops.draftMu.Unlock()
	if len(f.stops.draftStops) != 0 {
		t.Fatal("finished run retained stop registration")
	}
}

func TestDraftStorageIsBounded(t *testing.T) {
	f := &fakeDraftChannel{}
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx := context.Background()
	draftDelta(ctx, s, strings.Repeat("a", telegramDraftMaxBytes+1))
	s.mu.Lock()
	if !s.draft.disabled || s.draft.text.Len() != 0 {
		t.Fatal("oversized draft was retained")
	}
	s.mu.Unlock()
	s.Finish(ctx, agent.ReplyResult{Text: "Complete answer"})
}

func TestFinalWaitsForInFlightDraft(t *testing.T) {
	f := &fakeDraftChannel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx := context.Background()
	draftDelta(ctx, s, "Partial")
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("draft request did not start")
	}
	done := make(chan struct{})
	go func() {
		s.Finish(ctx, agent.ReplyResult{Text: "Final"})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("final overtook in-flight draft")
	case <-time.After(10 * time.Millisecond):
	}
	close(f.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("final did not finish after draft")
	}
}

func TestSendTextDraftUsesSafeRichMarkdown(t *testing.T) {
	caller := &mockAPICaller{}
	c := newTestChannelWithCaller(t, caller)
	text := "**Safe**\n\n<tg-button type=\"callback\" data=\"steal\">Click</tg-button>\n\n![image](https://example.com/a.png)\n\n| Key | Value |\n| --- | --- |\n| A | B |"
	if err := c.SendTextDraft(context.Background(), "123", 42, text, true); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || !strings.HasSuffix(caller.calls[0].url, "/sendRichMessageDraft") {
		t.Fatalf("calls = %#v", caller.calls)
	}
	body := caller.calls[0].body
	rich := body["rich_message"].(map[string]any)
	markdown := rich["markdown"].(string)
	if strings.Contains(markdown, "<tg-button") || strings.Contains(markdown, "![image]") ||
		!strings.Contains(markdown, "<table") {
		t.Fatalf("unsafe or unrendered draft Markdown = %q", markdown)
	}
	if rich["skip_entity_detection"] != true || body["draft_id"] != float64(42) ||
		body["can_stop"] != true ||
		body["keep_on_stop"] == true {
		t.Fatalf("draft flags = %#v", body)
	}
}

func TestSendTextDraftFallsBackWithoutSendingPermanentChunks(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", telegramDraftMaxBytes+1), strings.Repeat("x", telegramRichTextRunes+1)} {
		caller := &mockAPICaller{}
		c := newTestChannelWithCaller(t, caller)
		if err := c.SendTextDraft(context.Background(), "123", 1, text, false); !errors.Is(
			err,
			errDraftUnavailable,
		) {
			t.Fatalf("SendTextDraft error = %v, want unavailable", err)
		}
		if len(caller.calls) != 0 {
			t.Fatalf("unavailable draft emitted requests: %#v", caller.calls)
		}
	}
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{Ok: false, Error: &ta.Error{ErrorCode: 429, Description: "slow down"}},
		},
	}
	c := newTestChannelWithCaller(t, caller)
	if err := c.SendTextDraft(context.Background(), "123", 1, "Partial", false); err == nil ||
		len(caller.calls) != 1 {
		t.Fatalf("failed draft error=%v calls=%d", err, len(caller.calls))
	}
}
