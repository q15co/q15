package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type fakeAgentRunChannel struct {
	mu sync.Mutex

	sendTexts        []string
	sendTextChatIDs  []string
	sendMessageTexts []string
	sendPhotos       []fakeSentPhoto
	sendAudios       []fakeSentAudio
	editTexts        []string
	deletedMessages  []string
	reactions        []string
	clearReactions   int
	typingStarts     int
	typingStops      int
	nextMessageID    int
	editErr          error
	deleteErr        error
	sendErr          error
	sendMessageErr   error
	sendPhotoErr     error
	sendAudioErr     error

	sendCaptionedMedia   []fakeSentCaptionedMedia
	sendUncaptionedMedia []fakeSentUncaptionedMedia
}

type fakeSentPhoto struct {
	chatID   string
	mediaRef string
	caption  string
}

type fakeSentAudio struct {
	chatID   string
	mediaRef string
	caption  string
}

type fakeSentCaptionedMedia struct {
	kind     string
	chatID   string
	mediaRef string
	caption  string
}

type fakeSentUncaptionedMedia struct {
	kind     string
	chatID   string
	mediaRef string
}

func (f *fakeAgentRunChannel) SendText(
	ctx context.Context,
	chatID string,
	text string,
) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendTexts = append(f.sendTexts, text)
	f.sendTextChatIDs = append(f.sendTextChatIDs, chatID)
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

func (f *fakeAgentRunChannel) SendPhoto(
	ctx context.Context,
	chatID string,
	mediaRef string,
	caption string,
) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendPhotos = append(f.sendPhotos, fakeSentPhoto{
		chatID:   chatID,
		mediaRef: mediaRef,
		caption:  caption,
	})
	return f.sendPhotoErr
}

func (f *fakeAgentRunChannel) SendAudio(
	ctx context.Context,
	chatID string,
	mediaRef string,
	caption string,
) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendAudios = append(f.sendAudios, fakeSentAudio{
		chatID:   chatID,
		mediaRef: mediaRef,
		caption:  caption,
	})
	return f.sendAudioErr
}

func (f *fakeAgentRunChannel) SendVideo(
	ctx context.Context,
	chatID string,
	mediaRef string,
	caption string,
) error {
	return f.sendCaptioned(ctx, "video", chatID, mediaRef, caption)
}

func (f *fakeAgentRunChannel) SendDocument(
	ctx context.Context,
	chatID string,
	mediaRef string,
	caption string,
) error {
	return f.sendCaptioned(ctx, "document", chatID, mediaRef, caption)
}

func (f *fakeAgentRunChannel) SendAnimation(
	ctx context.Context,
	chatID string,
	mediaRef string,
	caption string,
) error {
	return f.sendCaptioned(ctx, "animation", chatID, mediaRef, caption)
}

func (f *fakeAgentRunChannel) SendVideoNote(
	ctx context.Context,
	chatID string,
	mediaRef string,
) error {
	return f.sendUncaptioned(ctx, "video_note", chatID, mediaRef)
}

func (f *fakeAgentRunChannel) SendSticker(
	ctx context.Context,
	chatID string,
	mediaRef string,
) error {
	return f.sendUncaptioned(ctx, "sticker", chatID, mediaRef)
}

func (f *fakeAgentRunChannel) sendCaptioned(
	_ context.Context,
	kind string,
	chatID string,
	mediaRef string,
	caption string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCaptionedMedia = append(f.sendCaptionedMedia, fakeSentCaptionedMedia{
		kind:     kind,
		chatID:   chatID,
		mediaRef: mediaRef,
		caption:  caption,
	})
	return f.sendErr
}

func (f *fakeAgentRunChannel) sendUncaptioned(
	_ context.Context,
	kind string,
	chatID string,
	mediaRef string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendUncaptionedMedia = append(f.sendUncaptionedMedia, fakeSentUncaptionedMedia{
		kind:     kind,
		chatID:   chatID,
		mediaRef: mediaRef,
	})
	return f.sendErr
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

func (f *fakeAgentRunChannel) DeleteMessage(
	ctx context.Context,
	chatID string,
	messageID string,
) error {
	_ = ctx
	_ = chatID
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedMessages = append(f.deletedMessages, messageID)
	return f.deleteErr
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

func TestAgentEndpointDeliverSendsText(t *testing.T) {
	t.Parallel()

	channel := &fakeAgentRunChannel{}
	endpoint := newAgentEndpoint(channel)

	err := endpoint.Deliver(context.Background(), bus.OutboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "chat-123",
		Text:    "scheduled result",
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendTexts) != 1 || channel.sendTexts[0] != "scheduled result" {
		t.Fatalf("sendTexts = %#v, want [scheduled result]", channel.sendTexts)
	}
	if len(channel.sendTextChatIDs) != 1 || channel.sendTextChatIDs[0] != "chat-123" {
		t.Fatalf("sendTextChatIDs = %#v, want [chat-123]", channel.sendTextChatIDs)
	}
}

func TestAgentEndpointDeliverReturnsSendError(t *testing.T) {
	t.Parallel()

	want := errors.New("send failed")
	endpoint := newAgentEndpoint(&fakeAgentRunChannel{sendErr: want})

	err := endpoint.Deliver(context.Background(), bus.OutboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "chat-123",
		Text:    "scheduled result",
	})
	if !errors.Is(err, want) {
		t.Fatalf("Deliver() error = %v, want %v", err, want)
	}
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

func TestAgentRunSession_TableFinalEditsPlaceholder(t *testing.T) {
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

			finalText := "| name | value |\n|---|---|\n| q15 | ok |"
			session.Finish(ctx, agent.ReplyResult{Text: finalText})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.deletedMessages) != 0 {
				t.Fatalf("deletedMessages = %#v, want none", channel.deletedMessages)
			}
			if len(channel.sendTexts) != 0 {
				t.Fatalf("sendTexts = %#v, want none", channel.sendTexts)
			}
			if len(channel.editTexts) != 1 || channel.editTexts[0] != finalText {
				t.Fatalf("editTexts = %#v, want [%q]", channel.editTexts, finalText)
			}
		},
	)
}

func TestAgentRunSession_TableFinalPreservesPlaceholderOnSendFailure(t *testing.T) {
	channel := &fakeAgentRunChannel{
		editErr: errors.New("edit failed"),
		sendErr: errors.New("send failed"),
	}
	session := newAgentRunSession(channel, "123", "42", progressModeQuiet)

	session.sendFinalText(
		context.Background(),
		"status-message",
		"| name | value |\n|---|---|\n| q15 | ok |",
	)

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.deletedMessages) != 0 {
		t.Fatalf("deletedMessages = %#v, want none", channel.deletedMessages)
	}
	if len(channel.sendTexts) != 1 {
		t.Fatalf("sendTexts = %#v, want one fallback attempt", channel.sendTexts)
	}
}

func TestAgentRunSession_DoesNotDuplicatePartiallyDeliveredFinal(t *testing.T) {
	channel := &fakeAgentRunChannel{editErr: &partialTextDeliveryError{
		err: errors.New("continuation failed"),
	}}
	session := newAgentRunSession(channel, "123", "42", progressModeQuiet)

	session.sendFinalText(context.Background(), "status-message", "final response")

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendTexts) != 0 {
		t.Fatalf("sendTexts = %#v, want no whole-response retry", channel.sendTexts)
	}
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

func TestAgentRunSession_LargeRichFinalEditsPlaceholder(t *testing.T) {
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
			if channel.editTexts[0] != longText {
				t.Fatalf(
					"edited text length = %d, want %d",
					len(channel.editTexts[0]),
					len(longText),
				)
			}
			if len(channel.sendMessageTexts) != 1 {
				t.Fatalf(
					"sendMessageTexts len = %d, want only placeholder",
					len(channel.sendMessageTexts),
				)
			}
		},
	)
}

func TestAgentRunSession_SingleImageReplyUsesCaptionWithoutPlaceholder(t *testing.T) {
	channel := &fakeAgentRunChannel{}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		Text:        "done",
		Attachments: []conversation.Part{conversation.Image("media://sha256/reply", "")},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendPhotos) != 1 {
		t.Fatalf("sendPhotos len = %d, want 1", len(channel.sendPhotos))
	}
	if got := channel.sendPhotos[0]; got.mediaRef != "media://sha256/reply" ||
		got.caption != "done" {
		t.Fatalf("sendPhotos[0] = %#v, want captioned photo", got)
	}
	if len(channel.sendTexts) != 0 {
		t.Fatalf("sendTexts = %#v, want none", channel.sendTexts)
	}
}

func TestAgentRunSession_MultiImageReplySendsTextAndPhotosSeparately(t *testing.T) {
	channel := &fakeAgentRunChannel{}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		Text: "done",
		MediaRefs: []string{
			"media://sha256/one",
			"media://sha256/two",
		},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendTexts) != 1 || channel.sendTexts[0] != "done" {
		t.Fatalf("sendTexts = %#v, want [done]", channel.sendTexts)
	}
	if len(channel.sendPhotos) != 2 {
		t.Fatalf("sendPhotos len = %d, want 2", len(channel.sendPhotos))
	}
	for i, photo := range channel.sendPhotos {
		if photo.caption != "" {
			t.Fatalf("sendPhotos[%d].caption = %q, want empty", i, photo.caption)
		}
	}
}

func TestAgentRunSession_AudioReplySendsTextAndAudioSeparately(t *testing.T) {
	channel := &fakeAgentRunChannel{}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		Text:        "done",
		Attachments: []conversation.Part{conversation.Audio("media://sha256/audio")},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendTexts) != 1 || channel.sendTexts[0] != "done" {
		t.Fatalf("sendTexts = %#v, want [done]", channel.sendTexts)
	}
	if len(channel.sendAudios) != 1 {
		t.Fatalf("sendAudios len = %d, want 1", len(channel.sendAudios))
	}
	if got := channel.sendAudios[0]; got.mediaRef != "media://sha256/audio" ||
		got.caption != "" {
		t.Fatalf("sendAudios[0] = %#v, want uncaptained audio", got)
	}
	if len(channel.sendPhotos) != 0 {
		t.Fatalf("sendPhotos = %#v, want none", channel.sendPhotos)
	}
}

func TestAgentRunSession_NewMediaKindsRouteToCorrectSendMethod(t *testing.T) {
	tests := []struct {
		name      string
		part      conversation.Part
		wantKind  string
		captioned bool
	}{
		{
			name:      "video",
			part:      conversation.Media(conversation.MediaKindVideo, "media://sha256/video"),
			wantKind:  "video",
			captioned: true,
		},
		{
			name:      "document",
			part:      conversation.Media(conversation.MediaKindDocument, "media://sha256/doc"),
			wantKind:  "document",
			captioned: true,
		},
		{
			name:      "animation",
			part:      conversation.Media(conversation.MediaKindAnimation, "media://sha256/anim"),
			wantKind:  "animation",
			captioned: true,
		},
		{
			name:      "sticker",
			part:      conversation.Media(conversation.MediaKindSticker, "media://sha256/sticker"),
			wantKind:  "sticker",
			captioned: false,
		},
		{
			name:      "video_note",
			part:      conversation.Media(conversation.MediaKindVideoNote, "media://sha256/vnote"),
			wantKind:  "video_note",
			captioned: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "", progressModeProgress)

			session.Finish(context.Background(), agent.ReplyResult{
				Attachments: []conversation.Part{tt.part},
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if tt.captioned {
				if len(channel.sendCaptionedMedia) != 1 {
					t.Fatalf("sendCaptionedMedia len = %d, want 1", len(channel.sendCaptionedMedia))
				}
				got := channel.sendCaptionedMedia[0]
				if got.kind != tt.wantKind {
					t.Fatalf("sendCaptionedMedia[0].kind = %q, want %q", got.kind, tt.wantKind)
				}
				if len(channel.sendUncaptionedMedia) != 0 {
					t.Fatalf("sendUncaptionedMedia = %#v, want none", channel.sendUncaptionedMedia)
				}
			} else {
				if len(channel.sendUncaptionedMedia) != 1 {
					t.Fatalf("sendUncaptionedMedia len = %d, want 1", len(channel.sendUncaptionedMedia))
				}
				got := channel.sendUncaptionedMedia[0]
				if got.kind != tt.wantKind {
					t.Fatalf("sendUncaptionedMedia[0].kind = %q, want %q", got.kind, tt.wantKind)
				}
				if len(channel.sendCaptionedMedia) != 0 {
					t.Fatalf("sendCaptionedMedia = %#v, want none", channel.sendCaptionedMedia)
				}
			}
		})
	}
}

func TestAgentRunSession_SingleCaptionableAttachmentUsesCaption(t *testing.T) {
	tests := []struct {
		name string
		part conversation.Part
	}{
		{
			name: "video",
			part: conversation.Media(conversation.MediaKindVideo, "media://sha256/video"),
		},
		{
			name: "document",
			part: conversation.Media(conversation.MediaKindDocument, "media://sha256/doc"),
		},
		{
			name: "animation",
			part: conversation.Media(conversation.MediaKindAnimation, "media://sha256/anim"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "", progressModeProgress)

			session.Finish(context.Background(), agent.ReplyResult{
				Text:        "done",
				Attachments: []conversation.Part{tt.part},
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.sendCaptionedMedia) != 1 {
				t.Fatalf("sendCaptionedMedia len = %d, want 1", len(channel.sendCaptionedMedia))
			}
			if got := channel.sendCaptionedMedia[0]; got.caption != "done" {
				t.Fatalf("sendCaptionedMedia[0].caption = %q, want %q", got.caption, "done")
			}
			if len(channel.sendTexts) != 0 {
				t.Fatalf("sendTexts = %#v, want none", channel.sendTexts)
			}
		})
	}
}

func TestAgentRunSession_StickerAndVideoNoteNeverReceiveCaption(t *testing.T) {
	tests := []struct {
		name string
		part conversation.Part
	}{
		{
			name: "sticker",
			part: conversation.Media(conversation.MediaKindSticker, "media://sha256/sticker"),
		},
		{
			name: "video_note",
			part: conversation.Media(conversation.MediaKindVideoNote, "media://sha256/vnote"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &fakeAgentRunChannel{}
			session := newAgentRunSession(channel, "123", "", progressModeProgress)

			session.Finish(context.Background(), agent.ReplyResult{
				Text:        "done",
				Attachments: []conversation.Part{tt.part},
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.sendUncaptionedMedia) != 1 {
				t.Fatalf("sendUncaptionedMedia len = %d, want 1", len(channel.sendUncaptionedMedia))
			}
			if got := channel.sendUncaptionedMedia[0].kind; got != tt.name {
				t.Fatalf("sendUncaptionedMedia[0].kind = %q, want %q", got, tt.name)
			}
			if len(channel.sendTexts) != 1 || channel.sendTexts[0] != "done" {
				t.Fatalf("sendTexts = %#v, want [done]", channel.sendTexts)
			}
			if len(channel.sendCaptionedMedia) != 0 {
				t.Fatalf("sendCaptionedMedia = %#v, want none", channel.sendCaptionedMedia)
			}
		})
	}
}

func TestAgentRunSession_DedupByKeyAndRef(t *testing.T) {
	channel := &fakeAgentRunChannel{}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		Attachments: []conversation.Part{
			conversation.Media(conversation.MediaKindVideo, "media://sha256/same"),
			conversation.Media(conversation.MediaKindDocument, "media://sha256/same"),
		},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendCaptionedMedia) != 2 {
		t.Fatalf(
			"sendCaptionedMedia len = %d, want 2 (same ref, different kinds are not deduped)",
			len(channel.sendCaptionedMedia),
		)
	}
	kinds := map[string]bool{}
	for _, sent := range channel.sendCaptionedMedia {
		kinds[sent.kind] = true
	}
	if !kinds["video"] || !kinds["document"] {
		t.Fatalf("sendCaptionedMedia kinds = %v, want both video and document", kinds)
	}
}

func TestAgentRunSession_ImageOnlyReplyDeletesPlaceholderBeforeSendingPhoto(t *testing.T) {
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

			session.Finish(ctx, agent.ReplyResult{
				MediaRefs: []string{"media://sha256/reply"},
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.deletedMessages) != 1 || channel.deletedMessages[0] != "1" {
				t.Fatalf("deletedMessages = %#v, want [1]", channel.deletedMessages)
			}
			if len(channel.sendPhotos) != 1 {
				t.Fatalf("sendPhotos len = %d, want 1", len(channel.sendPhotos))
			}
			if len(channel.editTexts) != 0 {
				t.Fatalf("editTexts = %#v, want none", channel.editTexts)
			}
		},
	)
}

func TestAgentRunSession_PlaceholderTextReplyCanStillSendPhoto(t *testing.T) {
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

			session.Finish(ctx, agent.ReplyResult{
				Text:      "done",
				MediaRefs: []string{"media://sha256/reply"},
			})

			channel.mu.Lock()
			defer channel.mu.Unlock()
			if len(channel.editTexts) != 1 || channel.editTexts[0] != "done" {
				t.Fatalf("editTexts = %#v, want [done]", channel.editTexts)
			}
			if len(channel.sendPhotos) != 1 || channel.sendPhotos[0].caption != "" {
				t.Fatalf("sendPhotos = %#v, want one uncaptained photo", channel.sendPhotos)
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

func TestAgentRunSession_PhotoSendFailureFallsBackToTextNotice(t *testing.T) {
	channel := &fakeAgentRunChannel{sendPhotoErr: errors.New("boom")}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		MediaRefs: []string{"media://sha256/reply"},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendPhotos) != 1 {
		t.Fatalf("sendPhotos len = %d, want 1", len(channel.sendPhotos))
	}
	if len(channel.sendTexts) != 1 || channel.sendTexts[0] != attachmentSendFailureText {
		t.Fatalf(
			"sendTexts = %#v, want [%q]",
			channel.sendTexts,
			attachmentSendFailureText,
		)
	}
}

func TestAgentRunSession_AudioSendFailureFallsBackToTextNotice(t *testing.T) {
	channel := &fakeAgentRunChannel{sendAudioErr: errors.New("boom")}
	session := newAgentRunSession(channel, "123", "", progressModeProgress)

	session.Finish(context.Background(), agent.ReplyResult{
		Attachments: []conversation.Part{conversation.Audio("media://sha256/audio")},
	})

	channel.mu.Lock()
	defer channel.mu.Unlock()
	if len(channel.sendAudios) != 1 {
		t.Fatalf("sendAudios len = %d, want 1", len(channel.sendAudios))
	}
	if len(channel.sendTexts) != 1 || channel.sendTexts[0] != attachmentSendFailureText {
		t.Fatalf(
			"sendTexts = %#v, want [%q]",
			channel.sendTexts,
			attachmentSendFailureText,
		)
	}
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
	if progressSummary != "💻 Running `git status --short`" {
		t.Fatalf(
			"progress summary = %q, want %q",
			progressSummary,
			"💻 Running `git status --short`",
		)
	}

	verboseSummary := summarizeToolCall(agent.ToolCall{
		Name:      "web_fetch",
		Arguments: `{"url":"https://example.com/path?q=1"}`,
	}, progressModeVerbose)
	if verboseSummary != "🌐 Fetching `example.com`" {
		t.Fatalf("verbose summary = %q, want %q", verboseSummary, "🌐 Fetching `example.com`")
	}

	fallbackSummary := summarizeToolCall(agent.ToolCall{Name: "custom_tool"}, progressModeProgress)
	if fallbackSummary != "⚙️ `custom tool`" {
		t.Fatalf("fallback summary = %q, want %q", fallbackSummary, "⚙️ `custom tool`")
	}
}

func TestToolProgressPreviewsAreConciseAndReadable(t *testing.T) {
	tests := []struct {
		name string
		call agent.ToolCall
		want string
	}{
		{
			"command",
			agent.ToolCall{Name: "exec", Arguments: `{"command":"go test\n\t./..."}`},
			"💻 Running `go test ./...`",
		},
		{
			"path",
			agent.ToolCall{Name: "read_file", Arguments: `{"path":"/workspace/a/../main.go"}`},
			"📖 Reading `/workspace/main.go`",
		},
		{
			"search",
			agent.ToolCall{Name: "web_search", Arguments: `{"query":"Go context cancellation"}`},
			"🌐 Searching for `Go context cancellation`",
		},
		{
			"fetch",
			agent.ToolCall{
				Name:      "web_fetch",
				Arguments: `{"url":"https://example.com/path?secret=hidden"}`,
			},
			"🌐 Fetching `example.com`",
		},
		{
			"poll",
			agent.ToolCall{Name: "exec_read", Arguments: `{"session_id":"sess-1"}`},
			"💻 Checking command `sess-1`",
		},
		{
			"input",
			agent.ToolCall{
				Name:      "exec_write",
				Arguments: `{"session_id":"sess-1","data":"never show input"}`,
			},
			"💻 Sending command input `sess-1`",
		},
		{
			"stop",
			agent.ToolCall{Name: "exec_kill", Arguments: `{"session_id":"sess-1"}`},
			"💻 Stopping command `sess-1`",
		},
		{"invalid arguments", agent.ToolCall{Name: "exec", Arguments: "{"}, "💻 Running command"},
		{
			"code boundary",
			agent.ToolCall{
				Name:      "exec",
				Arguments: "{\"command\":\"echo `hello`\\u001b[31m\\u202eevil\"}",
			},
			"💻 Running `echo 'hello' [31m evil`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeToolCall(tt.call, progressModeProgress); got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
		})
	}
	command := agent.ToolCall{
		Name:      "exec",
		Arguments: `{"command":"` + strings.Repeat("界", 150) + `"}`,
	}
	progress := summarizeToolCall(command, progressModeProgress)
	verbose := summarizeToolCall(command, progressModeVerbose)
	if !utf8.ValidString(progress) || !utf8.ValidString(verbose) ||
		utf8.RuneCountInString(progress) >= utf8.RuneCountInString(verbose) ||
		utf8.RuneCountInString(verbose) > 110 {
		t.Fatalf("preview bounds: progress=%q verbose=%q", progress, verbose)
	}
	path := agent.ToolCall{
		Name:      "read_file",
		Arguments: `{"path":"/` + strings.Repeat("long/", 100) + `main.go"}`,
	}
	if got := summarizeToolCall(path, progressModeProgress); !strings.HasSuffix(got, "main.go`") ||
		utf8.RuneCountInString(got) > 70 {
		t.Fatalf("path preview lost filename or exceeds bound: %q", got)
	}
}

func TestToolFinishedStatusDoesNotClaimCommandCompletion(t *testing.T) {
	if got := summarizeToolFinished(agent.ToolCall{Name: "exec"}, nil); got != "🧠 Reviewing result…" {
		t.Fatalf("finished summary = %q", got)
	}
	if got := summarizeToolFinished(agent.ToolCall{Name: "exec"}, errors.New("sensitive tool output")); got != "⚠️ Command step failed" {
		t.Fatalf("failed summary = %q", got)
	}
}
