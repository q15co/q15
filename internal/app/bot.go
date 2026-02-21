package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"q15.co/sandbox/internal/channel/telegram"
)

func RunBot(ctx context.Context) error {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is not set")
	}

	channel, err := telegram.NewChannel(token, func(message string) {
		fmt.Println(message)
	})
	if err != nil {
		return err
	}
	if err := channel.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}
