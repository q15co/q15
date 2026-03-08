// Package telegram implements the Telegram transport adapter.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
)

var (
	telegramProgressShowDelay    = 2 * time.Second
	telegramProgressSoftStall    = 10 * time.Second
	telegramProgressHardStall    = 30 * time.Second
	telegramProgressEditDebounce = 750 * time.Millisecond
)

type progressMode string

const (
	progressModeQuiet    progressMode = "quiet"
	progressModeProgress progressMode = "progress"
	progressModeVerbose  progressMode = "verbose"
)

const telegramAckReaction = "👀"

const (
	thinkingStatus      = "🧠 Thinking…"
	stillThinkingStatus = "🧠 Still thinking…"
	stoppedStatusPrefix = "⏹️ Stopped:"
)

type agentRunChannel interface {
	SendText(context.Context, string, string) error
	SendTextMessage(context.Context, string, string) (string, error)
	EditText(context.Context, string, string, string) error
	StartTyping(context.Context, string) (func(), error)
	SetReaction(context.Context, string, string, string) error
	ClearReaction(context.Context, string, string) error
}

// AgentEndpoint adapts the Telegram transport to the generic app worker.
type AgentEndpoint struct {
	channel agentRunChannel

	mu            sync.Mutex
	progressModes map[string]progressMode
}

// NewAgentEndpoint constructs a Telegram agent endpoint for the app worker.
func NewAgentEndpoint(channel *Channel) *AgentEndpoint {
	return newAgentEndpoint(channel)
}

func newAgentEndpoint(channel agentRunChannel) *AgentEndpoint {
	return &AgentEndpoint{
		channel:       channel,
		progressModes: make(map[string]progressMode),
	}
}

// Channel returns the transport name handled by this endpoint.
func (e *AgentEndpoint) Channel() string {
	return bus.ChannelTelegram
}

// OpenSession handles local Telegram controls or opens a run session.
func (e *AgentEndpoint) OpenSession(
	ctx context.Context,
	msg bus.InboundMessage,
) (channelport.AgentSession, error) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil, nil
	}

	handled, err := e.handleProgressCommand(ctx, msg)
	if handled || err != nil {
		return nil, err
	}

	session := newAgentRunSession(
		e.channel,
		msg.ChatID,
		msg.MessageID,
		e.progressMode(msg.ChatID),
	)
	session.start(ctx)
	return session, nil
}

func (e *AgentEndpoint) handleProgressCommand(
	ctx context.Context,
	msg bus.InboundMessage,
) (bool, error) {
	command, args, ok := parseTelegramCommand(msg.Text)
	if !ok || command != "progress" {
		return false, nil
	}

	current := e.progressMode(msg.ChatID)

	replyText := ""
	switch {
	case len(args) == 0:
		replyText = fmt.Sprintf(
			"Current progress mode: %s. Use /progress quiet|progress|verbose.",
			current,
		)
	case len(args) == 1:
		next, valid := parseProgressMode(args[0])
		if !valid {
			replyText = "Usage: /progress quiet|progress|verbose"
			break
		}
		e.setProgressMode(msg.ChatID, next)
		replyText = fmt.Sprintf("Progress mode set to %s.", next)
	default:
		replyText = "Usage: /progress quiet|progress|verbose"
	}

	return true, e.channel.SendText(ctx, msg.ChatID, replyText)
}

func (e *AgentEndpoint) progressMode(chatID string) progressMode {
	e.mu.Lock()
	defer e.mu.Unlock()

	mode := e.progressModes[strings.TrimSpace(chatID)]
	if mode == "" {
		return progressModeProgress
	}
	return mode
}

func (e *AgentEndpoint) setProgressMode(chatID string, mode progressMode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.progressModes[strings.TrimSpace(chatID)] = mode
}

func parseTelegramCommand(text string) (string, []string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil, false
	}
	first := strings.ToLower(strings.TrimSpace(fields[0]))
	if !strings.HasPrefix(first, "/") {
		return "", nil, false
	}
	if at := strings.Index(first, "@"); at >= 0 {
		first = first[:at]
	}
	if first == "/" {
		return "", nil, false
	}
	return strings.TrimPrefix(first, "/"), fields[1:], true
}

func parseProgressMode(raw string) (progressMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(progressModeQuiet):
		return progressModeQuiet, true
	case string(progressModeProgress):
		return progressModeProgress, true
	case string(progressModeVerbose):
		return progressModeVerbose, true
	default:
		return "", false
	}
}

type agentRunSession struct {
	channel   agentRunChannel
	chatID    string
	messageID string
	mode      progressMode

	opMu sync.Mutex
	mu   sync.Mutex

	typingStop      func()
	statusMessageID string
	lastSentStatus  string
	desiredStatus   string
	lastStep        string
	finished        bool

	showTimer     *time.Timer
	softTimer     *time.Timer
	hardTimer     *time.Timer
	debounceTimer *time.Timer
}

func newAgentRunSession(
	channel agentRunChannel,
	chatID string,
	messageID string,
	mode progressMode,
) *agentRunSession {
	return &agentRunSession{
		channel:   channel,
		chatID:    strings.TrimSpace(chatID),
		messageID: strings.TrimSpace(messageID),
		mode:      mode,
	}
}

func (s *agentRunSession) start(ctx context.Context) {
	if s.channel == nil {
		return
	}

	if stop, err := s.channel.StartTyping(ctx, s.chatID); err == nil {
		s.mu.Lock()
		s.typingStop = stop
		s.mu.Unlock()
	}
	if s.messageID != "" {
		if err := s.channel.SetReaction(ctx, s.chatID, s.messageID, telegramAckReaction); err != nil {
			s.logError("telegram reaction start error: %v", err)
		}
	}

	s.mu.Lock()
	s.resetStallTimersLocked(ctx)
	if s.mode != progressModeQuiet {
		s.showTimer = time.AfterFunc(telegramProgressShowDelay, func() {
			s.showStatus(ctx, s.currentWorkingText())
		})
	}
	s.mu.Unlock()
}

func (s *agentRunSession) OnRunEvent(ctx context.Context, event agent.RunEvent) {
	switch event.Type {
	case agent.RunEventModelTurnStarted:
		s.noteActivity(ctx, thinkingStatus)
	case agent.RunEventToolStarted:
		s.noteActivity(ctx, summarizeToolCall(event.ToolCall, s.mode))
	case agent.RunEventToolFinished:
		s.noteActivity(ctx, "")
	}
}

func (s *agentRunSession) Finish(ctx context.Context, finalText string) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	s.stopTimersLocked()
	statusMessageID := s.statusMessageID
	typingStop := s.typingStop
	s.mu.Unlock()

	if typingStop != nil {
		typingStop()
	}

	if strings.TrimSpace(finalText) == "" {
		finalText = "(assistant returned no text)"
	}

	s.opMu.Lock()
	if statusMessageID == "" {
		if err := s.channel.SendText(ctx, s.chatID, finalText); err != nil {
			s.logError("telegram final send error: %v", err)
		}
	} else {
		chunks := SplitText(finalText)
		if len(chunks) == 0 {
			chunks = []string{finalText}
		}

		editErr := s.channel.EditText(ctx, s.chatID, statusMessageID, chunks[0])
		if editErr != nil {
			s.logError("telegram placeholder edit error: %v", editErr)
			if err := s.channel.SendText(ctx, s.chatID, finalText); err != nil {
				s.logError("telegram final fallback send error: %v", err)
			}
		} else {
			for _, chunk := range chunks[1:] {
				if _, err := s.channel.SendTextMessage(ctx, s.chatID, chunk); err != nil {
					s.logError("telegram continuation send error: %v", err)
					break
				}
			}
		}
	}
	s.opMu.Unlock()

	if s.messageID != "" {
		if err := s.channel.ClearReaction(ctx, s.chatID, s.messageID); err != nil {
			s.logError("telegram reaction clear error: %v", err)
		}
	}
}

func (s *agentRunSession) Abort(ctx context.Context, reason string) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	s.stopTimersLocked()
	statusMessageID := s.statusMessageID
	typingStop := s.typingStop
	s.mu.Unlock()

	if typingStop != nil {
		typingStop()
	}

	if statusMessageID != "" && strings.TrimSpace(reason) != "" {
		s.opMu.Lock()
		if err := s.channel.EditText(ctx, s.chatID, statusMessageID, stoppedStatus(reason)); err != nil {
			s.logError("telegram stop status edit error: %v", err)
		}
		s.opMu.Unlock()
	}

	if s.messageID != "" {
		if err := s.channel.ClearReaction(ctx, s.chatID, s.messageID); err != nil {
			s.logError("telegram reaction clear error: %v", err)
		}
	}
}

func (s *agentRunSession) noteActivity(ctx context.Context, summary string) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if summary != "" {
		s.lastStep = summary
	}
	s.resetStallTimersLocked(ctx)
	hasStatus := s.statusMessageID != ""
	text := s.currentWorkingTextLocked()
	s.mu.Unlock()

	if hasStatus {
		s.scheduleStatusEdit(ctx, text)
	}
}

func (s *agentRunSession) showStatus(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	s.mu.Lock()
	if s.finished || s.statusMessageID != "" {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	if s.finished || s.statusMessageID != "" {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	messageID, err := s.channel.SendTextMessage(ctx, s.chatID, text)
	if err != nil {
		s.logError("telegram status send error: %v", err)
		return
	}

	s.mu.Lock()
	s.statusMessageID = messageID
	s.lastSentStatus = text
	s.desiredStatus = ""
	s.mu.Unlock()
}

func (s *agentRunSession) scheduleStatusEdit(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	s.mu.Lock()
	if s.finished || s.statusMessageID == "" || text == s.lastSentStatus {
		s.mu.Unlock()
		return
	}
	s.desiredStatus = text
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
	}
	s.debounceTimer = time.AfterFunc(telegramProgressEditDebounce, func() {
		s.flushStatusEdit(ctx)
	})
	s.mu.Unlock()
}

func (s *agentRunSession) flushStatusEdit(ctx context.Context) {
	s.mu.Lock()
	if s.finished || s.statusMessageID == "" {
		s.mu.Unlock()
		return
	}
	text := strings.TrimSpace(s.desiredStatus)
	lastSent := s.lastSentStatus
	s.mu.Unlock()

	if text == "" || text == lastSent {
		return
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	if s.finished || s.statusMessageID == "" {
		s.mu.Unlock()
		return
	}
	text = strings.TrimSpace(s.desiredStatus)
	messageID := s.statusMessageID
	lastSent = s.lastSentStatus
	s.mu.Unlock()

	if text == "" || text == lastSent {
		return
	}
	if err := s.channel.EditText(ctx, s.chatID, messageID, text); err != nil {
		s.logError("telegram status edit error: %v", err)
		return
	}

	s.mu.Lock()
	s.lastSentStatus = text
	s.mu.Unlock()
}

func (s *agentRunSession) resetStallTimersLocked(ctx context.Context) {
	if s.softTimer != nil {
		s.softTimer.Stop()
	}
	if s.hardTimer != nil {
		s.hardTimer.Stop()
	}

	if s.mode != progressModeQuiet {
		s.softTimer = time.AfterFunc(telegramProgressSoftStall, func() {
			s.showOrUpdateStatus(ctx, stillThinkingStatus)
		})
	}
	s.hardTimer = time.AfterFunc(telegramProgressHardStall, func() {
		s.showOrUpdateStatus(ctx, s.currentHardStallText())
	})
}

func (s *agentRunSession) showOrUpdateStatus(ctx context.Context, text string) {
	s.mu.Lock()
	hasStatus := s.statusMessageID != ""
	s.mu.Unlock()

	if hasStatus {
		s.scheduleStatusEdit(ctx, text)
		return
	}
	s.showStatus(ctx, text)
}

func (s *agentRunSession) currentWorkingText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentWorkingTextLocked()
}

func (s *agentRunSession) currentHardStallText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastStep == "" || s.lastStep == thinkingStatus {
		return stillThinkingStatus
	}
	return stillThinkingStatus + " Last step: " + s.lastStep
}

func (s *agentRunSession) currentWorkingTextLocked() string {
	if s.lastStep == "" {
		return thinkingStatus
	}
	return s.lastStep
}

func (s *agentRunSession) stopTimersLocked() {
	if s.showTimer != nil {
		s.showTimer.Stop()
		s.showTimer = nil
	}
	if s.softTimer != nil {
		s.softTimer.Stop()
		s.softTimer = nil
	}
	if s.hardTimer != nil {
		s.hardTimer.Stop()
		s.hardTimer = nil
	}
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
		s.debounceTimer = nil
	}
}

func (s *agentRunSession) logError(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func summarizeToolCall(call agent.ToolCall, mode progressMode) string {
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return thinkingStatus
	}

	switch name {
	case "read_file", "write_file", "edit_file":
		if path := normalizeDisplayPath(extractStringArg(call.Arguments, "path")); path != "" {
			switch name {
			case "read_file":
				return formatStatusMessage("📖", "Reading", path)
			case "write_file":
				return formatStatusMessage("✍️", "Writing", path)
			default:
				return formatStatusMessage("🛠️", "Editing", path)
			}
		}
		switch name {
		case "read_file":
			return "📖 Reading file"
		case "write_file":
			return "✍️ Writing file"
		default:
			return "🛠️ Editing file"
		}
	case "apply_patch":
		return "🩹 Applying patch"
	case "exec_nix_shell_bash":
		if mode == progressModeVerbose {
			if command := extractStringArg(call.Arguments, "command"); command != "" {
				return formatStatusMessage("💻", "Running", truncateSingleLine(command, 96))
			}
		}
		return "💻 Running command"
	case "exec_browser_shell":
		if mode == progressModeVerbose {
			if command := extractStringArg(call.Arguments, "command"); command != "" {
				return formatStatusMessage("🌐", "Running", truncateSingleLine(command, 96))
			}
		}
		return "🌐 Running browser command"
	case "web_fetch":
		if mode == progressModeVerbose {
			if rawURL := extractStringArg(call.Arguments, "url"); rawURL != "" {
				return formatStatusMessage("🌐", "Fetching", summarizeURL(rawURL))
			}
		}
		return "🌐 Fetching webpage"
	case "web_search":
		if mode == progressModeVerbose {
			if query := extractStringArg(call.Arguments, "query"); query != "" {
				return formatStatusMessage("🌐", "Searching for", truncateSingleLine(query, 96))
			}
		}
		return "🌐 Searching the web"
	default:
		return "⚙️ " + humanizeAction(name)
	}
}

func humanizeAction(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Working"
	}
	return name
}

func formatStatusMessage(emoji string, verb string, detail string) string {
	emoji = strings.TrimSpace(emoji)
	verb = strings.TrimSpace(verb)
	detail = strings.TrimSpace(detail)

	parts := make([]string, 0, 3)
	if emoji != "" {
		parts = append(parts, emoji)
	}
	if verb != "" {
		parts = append(parts, verb)
	}
	if detail != "" {
		parts = append(parts, inlineCode(detail))
	}
	return strings.Join(parts, " ")
}

func inlineCode(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(text, "`", "'") + "`"
}

func stoppedStatus(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return strings.TrimSuffix(stoppedStatusPrefix, ":")
	}
	return stoppedStatusPrefix + " " + reason
}

func extractStringArg(arguments string, key string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	raw, ok := payload[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func summarizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "webpage"
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return truncateSingleLine(raw, 96)
}

func truncateSingleLine(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func normalizeDisplayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}
