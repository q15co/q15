package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
)

type fakeAgentRunChannel struct {
	mu sync.Mutex

	sendTexts        []string
	sendMessageTexts []string
	editTexts        []string
	reactions        []string
	clearReactions   int
	typingStarts     int
	typingStops      int
	nextMessageID    int
	editErr          error
	sendErr          error
	sendMessageErr   error
}

func (f *fakeAgentRunChannel) SendText(
	ctx context.Context,
	chatID string,
	text string,
) error {
	_ = ctx
	_ = chatID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendTexts = append(f.sendTexts, text)
	return f.sendErr
}

func (f *fakeAgentRunChannel) SendTextMessage(
	ctx context.Context,
	chatID string,
	text string,
) (string, error) {
	_ = ctx
	_ = chatID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextMessageID++
	f.sendMessageTexts = append(f.sendMessageTexts, text)
	if f.sendMessageErr != nil {
		return "", f.sendMessageErr
	}
	return strconv.Itoa(f.nextMessageID), nil
}

func (f *fakeAgentRunChannel) EditText(
	ctx context.Context,
	chatID string,
	messageID string,
	text string,
) error {
	_ = ctx
	_ = chatID
	_ = messageID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editTexts = append(f.editTexts, text)
	return f.editErr
}

func (f *fakeAgentRunChannel) StartTyping(
	ctx context.Context,
	chatID string,
) (func(), error) {
	_ = ctx
	_ = chatID
	f.mu.Lock()
	f.typingStarts++
	f.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.typingStops++
			f.mu.Unlock()
		})
	}, nil
}

func (f *fakeAgentRunChannel) SetReaction(
	ctx context.Context,
	chatID string,
	messageID string,
	emoji string,
) error {
	_ = ctx
	_ = chatID
	_ = messageID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, emoji)
	return nil
}

func (f *fakeAgentRunChannel) ClearReaction(
	ctx context.Context,
	chatID string,
	messageID string,
) error {
	_ = ctx
	_ = chatID
	_ = messageID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearReactions++
	return nil
}

func withTelegramProgressDurations(
	show time.Duration,
	soft time.Duration,
	hard time.Duration,
	debounce time.Duration,
	fn func(),
) {
	prevShow := telegramProgressShowDelay
	prevSoft := telegramProgressSoftStall
	prevHard := telegramProgressHardStall
	prevDebounce := telegramProgressEditDebounce

	telegramProgressShowDelay = show
	telegramProgressSoftStall = soft
	telegramProgressHardStall = hard
	telegramProgressEditDebounce = debounce
	defer func() {
		telegramProgressShowDelay = prevShow
		telegramProgressSoftStall = prevSoft
		telegramProgressHardStall = prevHard
		telegramProgressEditDebounce = prevDebounce
	}()

	fn()
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestAgentEndpoint_OpenSession_HandlesProgressCommandLocally(t *testing.T) {
	channel := &fakeAgentRunChannel{}
	endpoint := newAgentEndpoint(channel)

	session, err := endpoint.OpenSession(context.Background(), bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "/progress verbose",
	})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if session != nil {
		t.Fatal("OpenSession() session should be nil for local command handling")
	}

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendTexts) != 1 {
		t.Fatalf("sendTexts len = %d, want 1", len(channel.sendTexts))
	}
	if got := channel.sendTexts[0]; got != "Progress mode set to verbose." {
		t.Fatalf("sendTexts[0] = %q, want %q", got, "Progress mode set to verbose.")
	}
}

func TestAgentEndpoint_OpenSession_UsesStoredProgressMode(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		time.Hour,
		time.Hour,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			endpoint := newAgentEndpoint(channel)
			ctx := context.Background()

			_, err := endpoint.OpenSession(ctx, bus.InboundMessage{
				Channel: bus.ChannelTelegram,
				ChatID:  "123",
				Text:    "/progress verbose",
			})
			if err != nil {
				t.Fatalf("OpenSession() command error = %v", err)
			}

			session, err := endpoint.OpenSession(ctx, bus.InboundMessage{
				Channel:   bus.ChannelTelegram,
				ChatID:    "123",
				MessageID: "42",
				Text:      "hello",
			})
			if err != nil {
				t.Fatalf("OpenSession() run error = %v", err)
			}
			if session == nil {
				t.Fatal("OpenSession() session = nil, want non-nil")
			}

			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			session.OnRunEvent(ctx, agent.RunEvent{
				Type: agent.RunEventToolStarted,
				ToolCall: agent.ToolCall{
					Name:      "exec",
					Arguments: `{"command":"git status --short"}`,
				},
			})

			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.editTexts) == 1
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if got := channel.editTexts[0]; got != "💻 Running `git status --short`" {
				t.Fatalf("editTexts[0] = %q, want %q", got, "💻 Running `git status --short`")
			}
		},
	)
}

func TestAgentRunSession_ProgressShowsPlaceholderAndEditsFinal(t *testing.T) {
	withTelegramProgressDurations(
		10*time.Millisecond,
		time.Hour,
		time.Hour,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "42", progressModeProgress)
			ctx := context.Background()

			session.start(ctx)

			time.Sleep(5 * time.Millisecond)
			channel.mu.Lock()
			if len(channel.sendMessageTexts) != 0 {
				channel.mu.Unlock()
				t.Fatalf(
					"status messages before show delay = %d, want 0",
					len(channel.sendMessageTexts),
				)
			}
			channel.mu.Unlock()

			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			session.Finish(ctx, agent.ReplyResult{Text: "done"})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if channel.typingStarts != 1 || channel.typingStops != 1 {
				t.Fatalf(
					"typing starts/stops = %d/%d, want 1/1",
					channel.typingStarts,
					channel.typingStops,
				)
			}
			if len(channel.reactions) != 1 || channel.reactions[0] != telegramAckReaction {
				t.Fatalf("reactions = %#v, want [%q]", channel.reactions, telegramAckReaction)
			}
			if channel.clearReactions != 1 {
				t.Fatalf("clear reactions = %d, want 1", channel.clearReactions)
			}
			if len(channel.sendMessageTexts) != 1 || channel.sendMessageTexts[0] != "🧠 Thinking…" {
				t.Fatalf("status messages = %#v, want [🧠 Thinking…]", channel.sendMessageTexts)
			}
			if len(channel.editTexts) != 1 || channel.editTexts[0] != "done" {
				t.Fatalf("edit texts = %#v, want [done]", channel.editTexts)
			}
		},
	)
}

func TestAgentRunSession_QuietWaitsForHardStall(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		20*time.Millisecond,
		40*time.Millisecond,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "42", progressModeQuiet)
			ctx := context.Background()

			session.start(ctx)

			time.Sleep(20 * time.Millisecond)
			channel.mu.Lock()
			if len(channel.sendMessageTexts) != 0 {
				channel.mu.Unlock()
				t.Fatalf(
					"quiet mode status messages before hard stall = %d, want 0",
					len(channel.sendMessageTexts),
				)
			}
			channel.mu.Unlock()

			waitForCondition(t, 250*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			channel.mu.Lock()
			if got := channel.sendMessageTexts[0]; got != "🧠 Still thinking…" {
				channel.mu.Unlock()
				t.Fatalf("hard stall message = %q, want %q", got, "🧠 Still thinking…")
			}
			channel.mu.Unlock()

			session.Finish(ctx, agent.ReplyResult{Text: "done"})
		},
	)
}

func TestAgentRunSession_DebouncesStatusEdits(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		time.Hour,
		time.Hour,
		20*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "42", progressModeVerbose)
			ctx := context.Background()

			session.start(ctx)
			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			session.OnRunEvent(ctx, agent.RunEvent{
				Type: agent.RunEventToolStarted,
				ToolCall: agent.ToolCall{
					Name:      "read_file",
					Arguments: `{"path":"/workspace/one.txt"}`,
				},
			})
			session.OnRunEvent(ctx, agent.RunEvent{
				Type: agent.RunEventToolStarted,
				ToolCall: agent.ToolCall{
					Name:      "exec",
					Arguments: `{"command":"echo hello"}`,
				},
			})

			time.Sleep(5 * time.Millisecond)
			channel.mu.Lock()
			if len(channel.editTexts) != 0 {
				channel.mu.Unlock()
				t.Fatalf("edit texts before debounce flush = %d, want 0", len(channel.editTexts))
			}
			channel.mu.Unlock()

			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.editTexts) == 1
			})

			channel.mu.Lock()
			if got := channel.editTexts[0]; got != "💻 Running `echo hello`" {
				channel.mu.Unlock()
				t.Fatalf("editTexts[0] = %q, want %q", got, "💻 Running `echo hello`")
			}
			channel.mu.Unlock()
		},
	)
}

func TestAgentRunSession_MultiChunkFinalKeepsPlaceholder(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		time.Hour,
		time.Hour,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "42", progressModeProgress)
			ctx := context.Background()

			session.start(ctx)
			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			longText := strings.Repeat("a", 3900) + " " + strings.Repeat("b", 3900)
			session.Finish(ctx, agent.ReplyResult{Text: longText})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.editTexts) != 1 {
				t.Fatalf("edit texts len = %d, want 1", len(channel.editTexts))
			}
			if len(channel.sendMessageTexts) < 2 {
				t.Fatalf(
					"sendMessageTexts len = %d, want at least 2",
					len(channel.sendMessageTexts),
				)
			}
		},
	)
}

func TestAgentRunSession_EditFailureFallsBackToNormalSend(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		time.Hour,
		time.Hour,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{editErr: errors.New("boom")}
			session := newAgentRunSession(channel, "123", "42", progressModeProgress)
			ctx := context.Background()

			session.start(ctx)
			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			session.Finish(ctx, agent.ReplyResult{Text: "done"})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.editTexts) != 1 {
				t.Fatalf("edit texts len = %d, want 1", len(channel.editTexts))
			}
			if len(channel.sendTexts) != 1 || channel.sendTexts[0] != "done" {
				t.Fatalf("fallback sends = %#v, want [done]", channel.sendTexts)
			}
		},
	)
}

func TestAgentRunSession_AbortClearsReactionAndEditsStatus(t *testing.T) {
	withTelegramProgressDurations(
		5*time.Millisecond,
		time.Hour,
		time.Hour,
		5*time.Millisecond,
		func() {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "42", progressModeProgress)
			ctx := context.Background()

			session.start(ctx)
			waitForCondition(t, 200*time.Millisecond, func() bool {
				channel.mu.Lock()
				defer channel.mu.Unlock()
				return len(channel.sendMessageTexts) == 1
			})

			session.Abort(ctx, "canceled")

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if channel.typingStops != 1 {
				t.Fatalf("typingStops = %d, want 1", channel.typingStops)
			}
			if channel.clearReactions != 1 {
				t.Fatalf("clearReactions = %d, want 1", channel.clearReactions)
			}
			if len(channel.editTexts) != 1 || channel.editTexts[0] != "⏹️ Stopped: canceled" {
				t.Fatalf("editTexts = %#v, want [⏹️ Stopped: canceled]", channel.editTexts)
			}
		},
	)
}

func TestSummarizeToolCall_ProgressAndVerboseModes(t *testing.T) {
	fileSummary := summarizeToolCall(agent.ToolCall{
		Name:      "read_file",
		Arguments: `{"path":" /workspace/foo/../bar.txt "}`,
	}, progressModeProgress)
	if fileSummary != "📖 Reading `/workspace/bar.txt`" {
		t.Fatalf("file summary = %q, want %q", fileSummary, "📖 Reading `/workspace/bar.txt`")
	}

	progressSummary := summarizeToolCall(agent.ToolCall{
		Name:      "exec",
		Arguments: `{"command":"git status --short"}`,
	}, progressModeProgress)
	if progressSummary != "💻 Running command" {
		t.Fatalf("progress summary = %q, want %q", progressSummary, "💻 Running command")
	}

	verboseSummary := summarizeToolCall(agent.ToolCall{
		Name:      "web_fetch",
		Arguments: `{"url":"https://example.com/path?q=1"}`,
	}, progressModeVerbose)
	if verboseSummary != "🌐 Fetching `example.com`" {
		t.Fatalf("verbose summary = %q, want %q", verboseSummary, "🌐 Fetching `example.com`")
	}

	fallbackSummary := summarizeToolCall(agent.ToolCall{Name: "custom_tool"}, progressModeProgress)
	if fallbackSummary != "⚙️ custom tool" {
		t.Fatalf("fallback summary = %q, want %q", fallbackSummary, "⚙️ custom tool")
	}
}
