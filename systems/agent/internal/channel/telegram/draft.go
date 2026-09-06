package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/mymmrac/telego"
)

var (
	telegramDraftUpdateInterval    = time.Second
	telegramDraftKeepaliveInterval = 20 * time.Second
	telegramDraftRequestTimeout    = 5 * time.Second
)

// Bound preview storage and rendering work independently of the full reply,
// which the engine retains and delivers through the normal final-message path.
const telegramDraftMaxBytes = 128 * 1024

var errDraftUnavailable = errors.New("telegram rich draft unavailable")

type draftChannel interface {
	SendTextDraft(context.Context, string, int, string, bool) error
	registerDraftStop(string, int, context.CancelFunc) func()
}

type draftKey struct {
	chatID  int64
	draftID int
}

// SendTextDraft applies the same untrusted Markdown boundary as final messages.
// A preview must fit one rich message; unsupported documents fall back to the
// session's progress indicator and complete final delivery.
func (c *Channel) SendTextDraft(
	ctx context.Context,
	chatID string,
	draftID int,
	text string,
	canStop bool,
) error {
	id, err := parseChatID(chatID)
	if err != nil || id <= 0 || draftID == 0 || len(text) > telegramDraftMaxBytes {
		return errDraftUnavailable
	}
	chunks := planRichText(text)
	if len(chunks) != 1 || !chunks[0].rich {
		return errDraftUnavailable
	}
	return c.bot.SendRichMessageDraft(ctx, &telego.SendRichMessageDraftParams{
		ChatID:  id,
		DraftID: draftID,
		RichMessage: telego.InputRichMessage{
			Markdown:            chunks[0].richMarkdown,
			SkipEntityDetection: true,
		},
		CanStop:    canStop,
		KeepOnStop: false,
	})
}

func (c *Channel) registerDraftStop(chatID string, draftID int, cancel context.CancelFunc) func() {
	id, err := parseChatID(chatID)
	if err != nil || id <= 0 || draftID == 0 || cancel == nil {
		return func() {}
	}
	key := draftKey{chatID: id, draftID: draftID}
	c.draftMu.Lock()
	if c.draftStops == nil {
		c.draftStops = make(map[draftKey]context.CancelFunc)
	}
	c.draftStops[key] = cancel
	c.draftMu.Unlock()
	return func() {
		c.draftMu.Lock()
		delete(c.draftStops, key)
		c.draftMu.Unlock()
	}
}

func (c *Channel) handleStoppedMessageGeneration(stopped telego.MessageGenerationStopped) {
	// Telegram omits a sender on this update. Drafts are private-chat only, so
	// authenticate using the chat's user ID and require an exact active draft.
	if stopped.Chat.Type != telego.ChatTypePrivate || stopped.Chat.ID <= 0 ||
		stopped.MessageThreadID != 0 {
		return
	}
	if len(c.allowedUserIDs) > 0 {
		if _, allowed := c.allowedUserIDs[stopped.Chat.ID]; !allowed {
			return
		}
	}
	key := draftKey{chatID: stopped.Chat.ID, draftID: stopped.DraftID}
	c.draftMu.Lock()
	cancel := c.draftStops[key]
	delete(c.draftStops, key)
	c.draftMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runDraft is guarded by agentRunSession.mu. Network operations share opMu
// with status and final sends, so no delayed draft can overwrite completion.
type runDraft struct {
	id             int
	channel        draftChannel
	text           strings.Builder
	status         string
	sent           bool
	inFlight       bool
	orphanStatusID string
	disabled       bool
	canStop        bool
	unregister     func()
	lastSend       time.Time
	timer          *time.Timer
	timerDue       time.Time
	generation     uint64
}

func newRunDraft(channel agentRunChannel, chatID string, mode progressMode) *runDraft {
	sender, ok := channel.(draftChannel)
	id, err := parseChatID(chatID)
	if !ok || err != nil || id <= 0 || mode == progressModeQuiet {
		return nil
	}
	// Random IDs prevent delayed stop updates from targeting a new run after
	// a restart, while staying within Telegram's signed 32-bit integer range.
	randomID, err := rand.Int(rand.Reader, big.NewInt(1<<31-1))
	if err != nil {
		return nil
	}
	draftID := int(randomID.Int64()) + 1
	return &runDraft{id: draftID, channel: sender}
}

// SetCancel binds Telegram's stop button to the worker-owned run context.
func (s *agentRunSession) SetCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.draft == nil || s.draft.disabled || cancel == nil {
		return
	}
	if s.draft.unregister != nil {
		s.draft.unregister()
	}
	s.draft.canStop = true
	s.draft.unregister = s.draft.channel.registerDraftStop(s.chatID, s.draft.id, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.finished {
			cancel()
		}
	})
}

func (s *agentRunSession) resetDraft(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.draft == nil || s.draft.disabled {
		return
	}
	s.draft.text.Reset()
	s.draft.status = thinkingStatus
	if s.draft.sent || s.draft.inFlight {
		s.scheduleDraftLocked(ctx, telegramDraftUpdateInterval)
	}
}

func (s *agentRunSession) appendDraft(ctx context.Context, delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.draft == nil || s.draft.disabled || ctx.Err() != nil {
		return
	}
	if len(delta) > telegramDraftMaxBytes-s.draft.text.Len() {
		s.disableDraftLocked()
		time.AfterFunc(0, func() { s.showOrUpdateStatus(ctx, s.currentWorkingText()) })
		return
	}
	s.draft.text.WriteString(delta)
	s.draft.status = ""
	// An existing timer retains the earliest permitted send time. New deltas
	// replace buffered content instead of queueing Telegram requests.
	s.scheduleDraftLocked(ctx, telegramDraftUpdateInterval)
}

func (s *agentRunSession) draftVisibleLocked() bool {
	return s.draft != nil && !s.draft.disabled &&
		(s.draft.sent || strings.TrimSpace(s.draft.text.String()) != "")
}

func (s *agentRunSession) scheduleDraftLocked(ctx context.Context, delay time.Duration) {
	d := s.draft
	due := d.lastSend.Add(delay)
	if d.timer != nil && !d.timerDue.After(due) {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.generation++
	generation := d.generation
	d.timerDue = due
	wait := time.Until(due)
	if wait < 0 {
		wait = 0
	}
	d.timer = time.AfterFunc(wait, func() { s.flushDraft(ctx, generation) })
}

func (s *agentRunSession) flushDraft(ctx context.Context, generation uint64) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	d := s.draft
	if s.finished || d == nil || d.disabled || d.generation != generation || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	d.timer = nil
	text := d.text.String()
	if d.status != "" {
		text = strings.TrimSpace(text + "\n\n" + d.status)
	}
	if strings.TrimSpace(text) == "" {
		s.mu.Unlock()
		return
	}
	d.lastSend = time.Now()
	d.inFlight = true
	canStop := d.canStop
	s.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, telegramDraftRequestTimeout)
	err := d.channel.SendTextDraft(requestCtx, s.chatID, d.id, text, canStop)
	cancel()
	s.mu.Lock()
	d.inFlight = false
	if err != nil {
		s.disableDraftLocked()
		s.mu.Unlock()
		s.logError("telegram draft send error: %v", err)
		// Do not hold opMu while creating the replacement progress message.
		time.AfterFunc(0, func() { s.showOrUpdateStatus(ctx, s.currentWorkingText()) })
		return
	}
	d.sent = true
	statusID := s.statusMessageID
	s.statusMessageID = ""
	s.lastSentStatus = ""
	if !d.disabled && d.timer == nil {
		s.scheduleDraftLocked(ctx, telegramDraftKeepaliveInterval)
	}
	disabled := d.disabled
	s.mu.Unlock()
	if !s.deleteStatusMessage(ctx, statusID) {
		s.mu.Lock()
		d.orphanStatusID = statusID
		s.mu.Unlock()
	}
	if disabled {
		time.AfterFunc(0, func() { s.showOrUpdateStatus(ctx, s.currentWorkingText()) })
	}
}

func (s *agentRunSession) stopDraftLocked() {
	if s.draft == nil {
		return
	}
	s.draft.generation++
	if s.draft.timer != nil {
		s.draft.timer.Stop()
		s.draft.timer = nil
	}
	if s.draft.unregister != nil {
		s.draft.unregister()
		s.draft.unregister = nil
	}
}

func (s *agentRunSession) disableDraftLocked() {
	s.stopDraftLocked()
	s.draft.disabled = true
	s.draft.text.Reset()
}
