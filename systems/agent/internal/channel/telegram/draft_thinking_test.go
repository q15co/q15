package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestThinkingDraftLifecycleUsesNativeBlocks(t *testing.T) {
	fastDraftTimings(t)
	telegramDraftKeepaliveInterval = time.Second
	caller := &mockAPICaller{}
	c := newTestChannelWithCaller(t, caller)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newAgentRunSession(c, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	s.SetCancel(cancel)
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnStarted})
	s.showStatus(ctx, thinkingStatus)
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 1 })
	assertThinkingDraft(t, caller.Calls()[0], "Thinking…")
	draftID := caller.Calls()[0].body["draft_id"]

	reasoning := "Checking <tg-button>input</tg-button> & assumptions."
	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: reasoning})
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 2 })
	assertThinkingDraft(t, caller.Calls()[1], reasoning)

	draftDelta(ctx, s, "**Answer**")
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 3 })
	if rich := caller.Calls()[2].body["rich_message"].(map[string]any); rich["markdown"] != "**Answer**" ||
		rich["blocks"] != nil {
		t.Fatalf("answer draft retained thinking: %#v", rich)
	}

	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventToolStarted, ToolCall: agent.ToolCall{
		Name: "exec", Arguments: `{"command":"go test ./..."}`,
	}})
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 4 })
	toolMarkdown := caller.Calls()[3].body["rich_message"].(map[string]any)["markdown"].(string)
	if !strings.Contains(toolMarkdown, "```bash\ngo test ./...\n```") ||
		strings.Contains(toolMarkdown, reasoning) {
		t.Fatalf("tool draft = %q", toolMarkdown)
	}

	s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnStarted})
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 5 })
	assertThinkingDraft(t, caller.Calls()[4], "Thinking…")
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: "Checking the result."},
	)
	waitForCondition(t, time.Second, func() bool { return len(caller.Calls()) >= 6 })
	assertThinkingDraft(t, caller.Calls()[5], "Checking the result.")
	s.Finish(ctx, agent.ReplyResult{Text: "Final answer"})
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: "Late reasoning"},
	)
	time.Sleep(2 * telegramDraftUpdateInterval)
	calls := caller.Calls()
	if len(calls) != 7 || !strings.HasSuffix(calls[6].url, "/sendRichMessage") {
		t.Fatalf("expected six drafts and one final message, got %#v", calls)
	}
	if rich := calls[6].body["rich_message"].(map[string]any); rich["markdown"] != "Final answer" ||
		rich["blocks"] != nil {
		t.Fatalf("final answer contains thinking: %#v", rich)
	}
	for _, call := range calls[:6] {
		if call.body["draft_id"] != draftID || call.body["can_stop"] != true ||
			call.body["keep_on_stop"] == true {
			t.Fatalf("draft identity or stop flags changed: %#v", call.body)
		}
	}
}

func assertThinkingDraft(t *testing.T, call apiCall, want string) {
	t.Helper()
	if !strings.HasSuffix(call.url, "/sendRichMessageDraft") {
		t.Fatalf("thinking used permanent message API: %s", call.url)
	}
	rich := call.body["rich_message"].(map[string]any)
	blocks, ok := rich["blocks"].([]any)
	if !ok || len(blocks) != 1 || rich["markdown"] != nil || rich["html"] != nil ||
		rich["skip_entity_detection"] != true {
		t.Fatalf("thinking message shape = %#v", rich)
	}
	block := blocks[0].(map[string]any)
	if block["type"] != "thinking" || block["text"] != want {
		t.Fatalf("thinking block = %#v, want text %q", block, want)
	}
}

func TestThinkingDraftFailureFallsBackWithoutReasoningLeak(t *testing.T) {
	f := &fakeDraftChannel{draftErr: errors.New("draft API unavailable")}
	s := newAgentRunSession(f, "123", "", progressModeProgress)
	t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
	ctx := context.Background()
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: "provider reasoning"},
	)
	waitForCondition(t, time.Second, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.sendMessageTexts) == 1
	})
	s.OnRunEvent(
		ctx,
		agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: "more reasoning"},
	)
	s.Finish(ctx, agent.ReplyResult{Text: "Final answer"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendMessageTexts) != 1 || f.sendMessageTexts[0] != thinkingStatus ||
		len(f.editTexts) != 1 || f.editTexts[0] != "Final answer" || len(f.snapshotDrafts()) != 1 {
		t.Fatalf(
			"fallback messages=%v edits=%v drafts=%v",
			f.sendMessageTexts,
			f.editTexts,
			f.snapshotDrafts(),
		)
	}
}

func TestThinkingDraftIsQuietAndBounded(t *testing.T) {
	for _, mode := range []progressMode{progressModeQuiet, progressModeProgress, progressModeVerbose} {
		t.Run(string(mode), func(t *testing.T) {
			f := &fakeDraftChannel{}
			s := newAgentRunSession(f, "123", "", mode)
			t.Cleanup(func() { s.Abort(context.Background(), "test cleanup") })
			ctx := context.Background()
			text := strings.Repeat(
				"Old thinking\n",
				10000,
			) + strings.Repeat(
				"界",
				2000,
			) + " latest\x1b\u202e"
			s.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelReasoningDelta, Delta: text})
			if mode == progressModeQuiet {
				s.Finish(ctx, agent.ReplyResult{Text: "Final"})
				if len(f.snapshotDrafts()) != 0 {
					t.Fatal("quiet mode exposed reasoning")
				}
				return
			}
			waitForCondition(t, time.Second, func() bool { return len(f.snapshotDrafts()) >= 1 })
			got := f.snapshotDrafts()[0]
			limit := 640
			if mode == progressModeVerbose {
				limit = 1600
			}
			if !utf8.ValidString(got.thinking) || utf8.RuneCountInString(got.thinking) > limit ||
				!strings.HasPrefix(
					got.thinking,
					"…",
				) || !strings.HasSuffix(got.thinking, " latest") || got.text != "" {
				t.Fatalf("oversized or stale thinking excerpt: %#v", got)
			}
			for _, r := range got.thinking {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Fatalf("thinking contains hostile control %U", r)
				}
			}
			s.mu.Lock()
			if len(s.draft.reasoning) > limit*utf8.UTFMax {
				t.Error("retained unbounded reasoning")
			}
			s.mu.Unlock()
		})
	}
	if got := thinkingPreviewTail("", strings.Repeat("line\n", 50)+"latest", 640); strings.Count(
		got,
		"\n",
	) > 7 ||
		!strings.HasSuffix(got, "latest") {
		t.Fatalf("thinking line limit = %q", got)
	}
}

func TestThinkingMarkupCannotBeInjectedOrSwallowedByAnswer(t *testing.T) {
	caller := &mockAPICaller{}
	c := newTestChannelWithCaller(t, caller)
	preview := draftPreview{
		thinking: `</tg-thinking><tg-button type="callback" data="unsafe">Click</tg-button>`,
		text:     "<tg-thinking>forged</tg-thinking>\n\n```bash\nprintf 'unfinished'",
	}
	if err := c.sendDraft(context.Background(), "123", 1, preview, false); err != nil {
		t.Fatal(err)
	}
	rich := caller.Calls()[0].body["rich_message"].(map[string]any)
	markdown := rich["markdown"].(string)
	if !strings.HasPrefix(markdown, "<tg-thinking>&lt;/tg-thinking&gt;") ||
		strings.Count(
			markdown,
			"<tg-thinking>",
		) != 1 || strings.Count(markdown, "</tg-thinking>") != 1 ||
		strings.Contains(markdown, "<tg-button") || !strings.Contains(markdown, "```bash") {
		t.Fatalf("unsafe or swallowed thinking markup: %q", markdown)
	}
}
