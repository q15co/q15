package telegram

import (
	"context"
	"errors"
	"fmt"
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

type Channel struct {
	bot       *telego.Bot
	onMessage MessageHandler
}

func NewChannel(token string, onMessage MessageHandler) (*Channel, error) {
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

	return &Channel{
		bot:       bot,
		onMessage: onMessage,
	}, nil
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

	_, err = c.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: id},
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	return nil
}
