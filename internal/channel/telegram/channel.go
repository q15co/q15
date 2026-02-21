package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type MessageHandler func(text string)

type Channel struct {
	bot       *telego.Bot
	onMessage MessageHandler
}

func NewChannel(token string, onMessage MessageHandler) (*Channel, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if onMessage == nil {
		onMessage = func(string) {}
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
	c.onMessage(message.Text)
	return nil
}
