// Package telegram implements the Telegram transport adapter.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

// IncomingMessage is a normalized Telegram update delivered to the app layer.
type IncomingMessage struct {
	ChatID    string
	UserID    string
	MessageID string
	SentAt    time.Time
	Text      string
	Media     []string
}

// MessageHandler processes one inbound Telegram message.
type MessageHandler func(msg IncomingMessage)

// Option mutates Telegram channel construction settings.
type Option func(*Channel) error

// Channel wraps the Telegram bot client and transport helpers.
type Channel struct {
	bot            *telego.Bot
	downloadClient *http.Client
	mediaStore     q15media.Store
	onMessage      MessageHandler
	allowedUserIDs map[int64]struct{}
}

var (
	telegramTypingKeepaliveInterval = 4 * time.Second
	telegramTextChunkRunes          = 3800
	telegramPhotoCaptionRunes       = 1024
	telegramLongPollTimeoutSeconds  = 30
	telegramLongPollRequestTimeout  = 45 * time.Second
	telegramLongPollRetryTimeout    = 8 * time.Second
	telegramLongPollRestartDelay    = 5 * time.Second
)

// NewChannel constructs a Telegram channel adapter.
func NewChannel(token string, onMessage MessageHandler, opts ...Option) (*Channel, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if onMessage == nil {
		onMessage = func(IncomingMessage) {}
	}

	bot, err := telego.NewBot(
		token,
		telego.WithHTTPClient(&http.Client{Timeout: telegramLongPollRequestTimeout}),
	)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	ch := &Channel{
		bot:            bot,
		downloadClient: http.DefaultClient,
		onMessage:      onMessage,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(ch); err != nil {
			return nil, err
		}
	}

	return ch, nil
}

// Start begins long polling and dispatches inbound Telegram messages.
func (c *Channel) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	go c.superviseLongPolling(ctx)
	return nil
}

func (c *Channel) superviseLongPolling(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// Telego owns retries inside one session. This loop only restarts
		// the session if the update channel or handler stops unexpectedly.
		err := c.runLongPollingSession(ctx)
		if err := ctx.Err(); err != nil {
			return
		}
		if err != nil {
			fmt.Fprintf(
				os.Stderr,
				"telegram long polling stopped: %v; restarting in %s\n",
				err,
				telegramLongPollRestartDelay,
			)
		} else {
			fmt.Fprintf(
				os.Stderr,
				"telegram long polling stopped unexpectedly; restarting in %s\n",
				telegramLongPollRestartDelay,
			)
		}
		if !sleepContext(ctx, telegramLongPollRestartDelay) {
			return
		}
	}
}

func (c *Channel) runLongPollingSession(ctx context.Context) error {
	params := &telego.GetUpdatesParams{
		Timeout:        telegramLongPollTimeoutSeconds,
		AllowedUpdates: []string{telego.MessageUpdates},
	}
	updates, err := c.bot.UpdatesViaLongPolling(
		ctx,
		params,
		telego.WithLongPollingRetryTimeout(telegramLongPollRetryTimeout),
	)
	if err != nil {
		return fmt.Errorf("start long polling: %w", err)
	}

	bh, err := th.NewBotHandler(c.bot, updates)
	if err != nil {
		return fmt.Errorf("create bot handler: %w", err)
	}

	bh.HandleMessage(func(ctx *th.Context, message telego.Message) error {
		return c.handleMessage(ctx, &message)
	}, th.AnyMessage())

	stopHandler := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = bh.Stop()
		case <-stopHandler:
		}
	}()

	// Start blocks until Telego's update channel closes or the handler stops.
	if err := bh.Start(); err != nil {
		close(stopHandler)
		return fmt.Errorf("run bot handler: %w", err)
	}
	close(stopHandler)
	return nil
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Channel) handleMessage(ctx context.Context, message *telego.Message) error {
	if len(c.allowedUserIDs) > 0 {
		if message.From == nil {
			fmt.Fprintln(
				os.Stdout,
				"ignore telegram message without sender while allowlist is enabled",
			)
			return nil
		}
		if _, ok := c.allowedUserIDs[message.From.ID]; !ok {
			fmt.Fprintf(os.Stdout, "ignore unauthorized telegram user %d\n", message.From.ID)
			return nil
		}
	}

	text := inputText(message)
	msg := IncomingMessage{
		ChatID:    strconv.FormatInt(message.Chat.ID, 10),
		MessageID: strconv.Itoa(message.MessageID),
		SentAt:    time.Unix(message.Date, 0).In(time.Local),
		Text:      text,
	}
	if message.From != nil {
		msg.UserID = strconv.FormatInt(message.From.ID, 10)
	}

	if len(message.Photo) > 0 {
		ref, err := c.storePhoto(
			ctx,
			message.Photo[len(message.Photo)-1].FileID,
			mediaScope(msg.ChatID, msg.MessageID),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "telegram photo ingest error: %v\n", err)
			msg.Text = attachmentFailureText("photo", text)
			msg.Media = nil
			c.onMessage(msg)
			return nil
		}
		msg.Media = []string{ref}
	} else if kind := unsupportedTelegramAttachmentKind(message); kind != "" {
		msg.Text = unsupportedAttachmentText(kind, text)
		msg.Media = nil
		c.onMessage(msg)
		return nil
	}

	if msg.Text == "" && len(msg.Media) == 0 {
		return nil
	}

	c.onMessage(msg)
	return nil
}

// SendText sends a possibly chunked Telegram text response.
func (c *Channel) SendText(ctx context.Context, chatID, text string) error {
	chatID = strings.TrimSpace(chatID)
	text = strings.TrimSpace(text)

	if chatID == "" {
		return errors.New("chat id is required")
	}
	if text == "" {
		return errors.New("text is required")
	}

	for _, chunk := range SplitText(text) {
		if _, err := c.SendTextMessage(ctx, chatID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// SendTextMessage sends one Telegram message and returns its message ID.
func (c *Channel) SendTextMessage(ctx context.Context, chatID, text string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	text = strings.TrimSpace(text)

	if chatID == "" {
		return "", errors.New("chat id is required")
	}
	if text == "" {
		return "", errors.New("text is required")
	}

	id, err := parseChatID(chatID)
	if err != nil {
		return "", fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}

	formatted := markdownToTelegramHTML(text)
	msg, err := c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: id},
		Text:      formatted,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		var plainErr error
		msg, plainErr = c.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: id},
			Text:   text,
		})
		if plainErr != nil {
			return "", fmt.Errorf("send telegram message: %w", plainErr)
		}
	}
	return strconv.Itoa(msg.MessageID), nil
}

// SendPhoto sends one Telegram photo resolved from a media-store ref.
func (c *Channel) SendPhoto(ctx context.Context, chatID, mediaRef, caption string) error {
	chatID = strings.TrimSpace(chatID)
	mediaRef = strings.TrimSpace(mediaRef)
	caption = strings.TrimSpace(caption)

	if chatID == "" {
		return errors.New("chat id is required")
	}
	if mediaRef == "" {
		return errors.New("media ref is required")
	}
	if c.mediaStore == nil {
		return errors.New("telegram media store is not configured")
	}
	if caption != "" && utf8.RuneCountInString(caption) > telegramPhotoCaptionRunes {
		return fmt.Errorf("telegram photo caption exceeds %d runes", telegramPhotoCaptionRunes)
	}

	chatValue, err := parseChatID(chatID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}

	localPath, meta, err := c.mediaStore.Resolve(mediaRef)
	if err != nil {
		return fmt.Errorf("resolve telegram photo %q: %w", mediaRef, err)
	}
	if err := ensureTelegramPhoto(localPath, meta); err != nil {
		return err
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open telegram photo %q: %w", localPath, err)
	}
	defer file.Close()

	params := &telego.SendPhotoParams{
		ChatID: telego.ChatID{ID: chatValue},
		Photo:  telego.InputFile{File: file},
	}
	if caption == "" {
		if _, err := c.bot.SendPhoto(ctx, params); err != nil {
			return fmt.Errorf("send telegram photo: %w", err)
		}
		return nil
	}

	formatted := markdownToTelegramHTML(caption)
	if formatted != "" && utf8.RuneCountInString(formatted) <= telegramPhotoCaptionRunes {
		params.Caption = formatted
		params.ParseMode = telego.ModeHTML
		if _, err := c.bot.SendPhoto(ctx, params); err == nil {
			return nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind telegram photo %q: %w", localPath, err)
		}
	}

	params.Caption = caption
	params.ParseMode = ""
	if _, err := c.bot.SendPhoto(ctx, params); err != nil {
		return fmt.Errorf("send telegram photo: %w", err)
	}
	return nil
}

// DeleteMessage removes one Telegram message.
func (c *Channel) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)

	if chatID == "" {
		return errors.New("chat id is required")
	}
	if messageID == "" {
		return errors.New("message id is required")
	}

	chatValue, err := parseChatID(chatID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}
	msgValue, err := parseMessageID(messageID)
	if err != nil {
		return fmt.Errorf("invalid message id %q: %w", messageID, err)
	}
	if err := c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatValue},
		MessageID: msgValue,
	}); err != nil {
		return fmt.Errorf("delete telegram message: %w", err)
	}
	return nil
}

// EditText edits one Telegram message in place.
func (c *Channel) EditText(ctx context.Context, chatID, messageID, text string) error {
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	text = strings.TrimSpace(text)

	if chatID == "" {
		return errors.New("chat id is required")
	}
	if messageID == "" {
		return errors.New("message id is required")
	}
	if text == "" {
		return errors.New("text is required")
	}

	chatValue, err := parseChatID(chatID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}
	msgValue, err := parseMessageID(messageID)
	if err != nil {
		return fmt.Errorf("invalid message id %q: %w", messageID, err)
	}

	formatted := markdownToTelegramHTML(text)
	_, err = c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: chatValue},
		MessageID: msgValue,
		Text:      formatted,
		ParseMode: telego.ModeHTML,
	})
	if err == nil {
		return nil
	}

	_, plainErr := c.bot.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID:    telego.ChatID{ID: chatValue},
		MessageID: msgValue,
		Text:      text,
	})
	if plainErr != nil {
		return fmt.Errorf("edit telegram message: %w", plainErr)
	}
	return nil
}

// StartTyping starts a typing keepalive and returns a stop function.
func (c *Channel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	chatValue, err := parseChatID(chatID)
	if err != nil {
		return func() {}, fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}

	typingCtx, cancel := context.WithCancel(ctx)
	send := func() {
		_ = c.bot.SendChatAction(
			typingCtx,
			&telego.SendChatActionParams{
				ChatID: telego.ChatID{ID: chatValue},
				Action: telego.ChatActionTyping,
			},
		)
	}

	send()
	go func() {
		ticker := time.NewTicker(telegramTypingKeepaliveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				send()
			}
		}
	}()

	return cancel, nil
}

// SetReaction sets a reaction on an existing Telegram message.
func (c *Channel) SetReaction(ctx context.Context, chatID, messageID, emoji string) error {
	chatValue, err := parseChatID(chatID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}
	msgValue, err := parseMessageID(messageID)
	if err != nil {
		return fmt.Errorf("invalid message id %q: %w", messageID, err)
	}

	params := &telego.SetMessageReactionParams{
		ChatID:    telego.ChatID{ID: chatValue},
		MessageID: msgValue,
	}
	if trimmed := strings.TrimSpace(emoji); trimmed != "" {
		params.Reaction = []telego.ReactionType{tu.ReactionEmoji(trimmed)}
	}
	return c.bot.SetMessageReaction(ctx, params)
}

// ClearReaction clears all reactions managed by this adapter on a message.
func (c *Channel) ClearReaction(ctx context.Context, chatID, messageID string) error {
	return c.SetReaction(ctx, chatID, messageID, "")
}

// WithMediaStore configures runtime-owned media storage for inbound Telegram media.
func WithMediaStore(store q15media.Store) Option {
	return func(c *Channel) error {
		if store == nil {
			return errors.New("telegram media store is required")
		}
		c.mediaStore = store
		return nil
	}
}

// WithAllowedUserIDs restricts inbound handling to a Telegram user allowlist.
func WithAllowedUserIDs(ids []int64) Option {
	return func(c *Channel) error {
		if len(ids) == 0 {
			return errors.New("telegram allowed user ids cannot be empty")
		}

		allowed := make(map[int64]struct{}, len(ids))
		for i, id := range ids {
			if id <= 0 {
				return fmt.Errorf("telegram allowed user ids[%d] must be greater than 0", i)
			}
			allowed[id] = struct{}{}
		}

		c.allowedUserIDs = allowed
		return nil
	}
}

func inputText(message *telego.Message) string {
	if message == nil {
		return ""
	}

	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(message.Text); text != "" {
		parts = append(parts, text)
	}
	if caption := strings.TrimSpace(message.Caption); caption != "" {
		parts = append(parts, caption)
	}
	return strings.Join(parts, "\n")
}

func mediaScope(chatID, messageID string) string {
	return "telegram:" + strings.TrimSpace(chatID) + ":" + strings.TrimSpace(messageID)
}

func unsupportedTelegramAttachmentKind(message *telego.Message) string {
	if message == nil {
		return ""
	}

	switch {
	case message.Animation != nil:
		return "animation"
	case message.Audio != nil:
		return "audio"
	case message.Document != nil:
		return "document"
	case message.Sticker != nil:
		return "sticker"
	case message.Video != nil:
		return "video"
	case message.VideoNote != nil:
		return "video note"
	case message.Voice != nil:
		return "voice"
	default:
		return ""
	}
}

func unsupportedAttachmentText(kind, originalText string) string {
	return attachmentNotice(
		fmt.Sprintf("The user sent a Telegram %s attachment.", strings.TrimSpace(kind)),
		originalText,
	)
}

func attachmentFailureText(kind, originalText string) string {
	return attachmentNotice(
		fmt.Sprintf(
			"The user sent a Telegram %s attachment, but q15 could not load it.",
			strings.TrimSpace(kind),
		),
		originalText,
	)
}

func attachmentNotice(summary, originalText string) string {
	lines := []string{
		"System note: " + strings.TrimSpace(summary),
		"Telegram currently supports photos/images only.",
		"The agent must not pretend it saw the attachment.",
	}
	originalText = strings.TrimSpace(originalText)
	if originalText != "" {
		lines = append(lines, "Original user text: "+originalText)
	}
	return strings.Join(lines, "\n")
}

func ensureTelegramPhoto(localPath string, meta q15media.Meta) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat telegram photo %q: %w", localPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("telegram photo %q must be a file", localPath)
	}

	if contentType := strings.ToLower(strings.TrimSpace(meta.ContentType)); contentType != "" {
		if !strings.HasPrefix(contentType, "image/") {
			return fmt.Errorf(
				"media object %q is not an image (content type %q)",
				localPath,
				contentType,
			)
		}
		return nil
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open media object %q: %w", localPath, err)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read media object header %q: %w", localPath, err)
	}

	contentType := strings.ToLower(http.DetectContentType(header[:n]))
	if !strings.HasPrefix(contentType, "image/") {
		return fmt.Errorf(
			"media object %q is not an image (detected %q)",
			localPath,
			contentType,
		)
	}
	return nil
}

func (c *Channel) storePhoto(ctx context.Context, fileID, scope string) (string, error) {
	if c.mediaStore == nil {
		return "", fmt.Errorf("telegram media store is not configured")
	}

	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", fmt.Errorf("telegram file id is required")
	}

	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("get telegram file %q: %w", fileID, err)
	}

	localPath, contentType, filename, err := c.downloadPhoto(ctx, file)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = os.Remove(localPath)
	}()

	ref, err := c.mediaStore.Store(localPath, q15media.Meta{
		Filename:    filename,
		ContentType: contentType,
		Source:      "telegram",
	}, scope)
	if err != nil {
		return "", fmt.Errorf("store telegram photo %q: %w", filename, err)
	}
	return ref, nil
}

func (c *Channel) downloadPhoto(
	ctx context.Context,
	file *telego.File,
) (localPath string, contentType string, filename string, err error) {
	if file == nil {
		return "", "", "", fmt.Errorf("telegram file is required")
	}
	if strings.TrimSpace(file.FilePath) == "" {
		return "", "", "", fmt.Errorf("telegram file path is required")
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.bot.FileDownloadURL(file.FilePath),
		nil,
	)
	if err != nil {
		return "", "", "", fmt.Errorf("build telegram file download request: %w", err)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("download telegram file %q: %w", file.FilePath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf(
			"download telegram file %q: unexpected status %s",
			file.FilePath,
			resp.Status,
		)
	}

	tmp, err := os.CreateTemp("", "q15-telegram-photo-*")
	if err != nil {
		return "", "", "", fmt.Errorf("create temp file for telegram photo: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err = io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return "", "", "", fmt.Errorf("write telegram photo to temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", "", "", fmt.Errorf("close telegram temp photo file: %w", err)
	}

	contentType, err = detectImageContentType(tmp.Name())
	if err != nil {
		return "", "", "", err
	}
	filename = normalizeFilename(file.FilePath, "photo.jpg")
	return tmp.Name(), contentType, filename, nil
}

func (c *Channel) httpClient() *http.Client {
	if c.downloadClient != nil {
		return c.downloadClient
	}
	return http.DefaultClient
}

func detectImageContentType(localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open telegram media file %q: %w", localPath, err)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read telegram media header %q: %w", localPath, err)
	}

	contentType := strings.ToLower(http.DetectContentType(header[:n]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf(
			"telegram media file %q is not an image (detected %q)",
			localPath,
			contentType,
		)
	}
	return contentType, nil
}

func normalizeFilename(filePath, fallback string) string {
	name := strings.TrimSpace(filepath.Base(strings.TrimSpace(filePath)))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return fallback
	}
	return name
}

// SplitText splits text into Telegram-safe chunks.
func SplitText(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if telegramTextChunkRunes <= 0 || utf8.RuneCountInString(text) <= telegramTextChunkRunes {
		return []string{text}
	}

	chunks := make([]string, 0, 4)
	for {
		chunk, rest := splitTextChunk(text, telegramTextChunkRunes)
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if rest == "" {
			break
		}
		text = rest
	}
	return chunks
}

func splitTextChunk(text string, limitRunes int) (string, string) {
	if limitRunes <= 0 || utf8.RuneCountInString(text) <= limitRunes {
		return strings.TrimSpace(text), ""
	}

	limitBytes := byteIndexAtRuneLimit(text, limitRunes)
	prefix := text[:limitBytes]

	type boundary struct {
		delimiter string
		skip      int
	}
	for _, candidate := range []boundary{
		{delimiter: "\n\n", skip: 2},
		{delimiter: "\n", skip: 1},
		{delimiter: " ", skip: 1},
	} {
		idx := strings.LastIndex(prefix, candidate.delimiter)
		if idx > limitBytes/2 {
			chunk := strings.TrimSpace(text[:idx])
			rest := strings.TrimSpace(text[idx+candidate.skip:])
			if chunk != "" {
				return chunk, rest
			}
		}
	}

	chunk := strings.TrimSpace(prefix)
	rest := strings.TrimSpace(text[limitBytes:])
	return chunk, rest
}

func byteIndexAtRuneLimit(text string, limitRunes int) int {
	if limitRunes <= 0 {
		return 0
	}
	runes := 0
	for idx := range text {
		if runes == limitRunes {
			return idx
		}
		runes++
	}
	return len(text)
}

func parseChatID(chatID string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
}

func parseMessageID(messageID string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(messageID))
}
