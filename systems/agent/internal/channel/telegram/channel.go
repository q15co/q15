package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type IncomingMessage struct {
	ChatID string
	UserID string
	Text   string
}

type MessageHandler func(msg IncomingMessage)

type Option func(*Channel) error

type Channel struct {
	bot            *telego.Bot
	onMessage      MessageHandler
	allowedUserIDs map[int64]struct{}
}

func NewChannel(token string, onMessage MessageHandler, opts ...Option) (*Channel, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if onMessage == nil {
		onMessage = func(IncomingMessage) {}
	}

	bot, err := telego.NewBot(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	ch := &Channel{
		bot:       bot,
		onMessage: onMessage,
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

func (c *Channel) Start(ctx context.Context) error {
	updates, err := c.bot.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout: 30,
	})
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

	go bh.Start()
	go func() {
		<-ctx.Done()
		bh.Stop()
	}()

	return nil
}

func (c *Channel) handleMessage(_ context.Context, message *telego.Message) error {
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil
	}

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

	msg := IncomingMessage{
		ChatID: strconv.FormatInt(message.Chat.ID, 10),
		Text:   text,
	}
	if message.From != nil {
		msg.UserID = strconv.FormatInt(message.From.ID, 10)
	}

	c.onMessage(msg)
	return nil
}

func (c *Channel) SendText(ctx context.Context, chatID, text string) error {
	chatID = strings.TrimSpace(chatID)
	text = strings.TrimSpace(text)

	if chatID == "" {
		return errors.New("chat id is required")
	}
	if text == "" {
		return errors.New("text is required")
	}

	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatID, err)
	}

	formatted := markdownToTelegramHTML(text)
	_, err = c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: id},
		Text:      formatted,
		ParseMode: telego.ModeHTML,
	})
	if err != nil {
		_, plainErr := c.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: telego.ChatID{ID: id},
			Text:   text,
		})
		if plainErr != nil {
			return fmt.Errorf("send telegram message: %w", plainErr)
		}
	}
	return nil
}

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
