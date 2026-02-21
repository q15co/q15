package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"q15.co/sandbox/internal/agent"
	"q15.co/sandbox/internal/bus"
	"q15.co/sandbox/internal/channel/telegram"
	"q15.co/sandbox/internal/provider/moonshot"
	"q15.co/sandbox/internal/tools"
)

func RunBot(ctx context.Context, modelName string) error {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		return errors.New("TELEGRAM_BOT_TOKEN is not set")
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return errors.New("model name is required")
	}

	modelAdapter := moonshot.NewClient()
	toolRunner := tools.NewShell()
	broker := bus.New(bus.DefaultBufferSize)

	var (
		mu     sync.Mutex
		agents = make(map[string]agent.Agent)
	)

	getAgent := func(sessionKey string) agent.Agent {
		mu.Lock()
		defer mu.Unlock()

		if a, ok := agents[sessionKey]; ok {
			return a
		}

		a := agent.NewLoop(modelAdapter, toolRunner, modelName, agent.DefaultSystemPrompt)
		agents[sessionKey] = a
		return a
	}

	channel, err := telegram.NewChannel(token, func(msg telegram.IncomingMessage) {
		err := broker.PublishInbound(ctx, bus.InboundMessage{
			Channel: bus.ChannelTelegram,
			ChatID:  msg.ChatID,
			UserID:  msg.UserID,
			Text:    msg.Text,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "publish inbound error: %v\n", err)
		}
	})
	if err != nil {
		return err
	}
	if err := channel.Start(ctx); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- runAgentWorker(ctx, broker, getAgent)
	}()
	go func() {
		errCh <- runTelegramSender(ctx, broker, channel)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func runAgentWorker(ctx context.Context, broker *bus.Broker, getAgent func(sessionKey string) agent.Agent) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-broker.Inbound():
			text := strings.TrimSpace(in.Text)
			if text == "" {
				continue
			}

			sessionKey, err := bus.SessionKey(in.Channel, in.ChatID)
			if err != nil {
				continue
			}

			a := getAgent(sessionKey)
			if text == "/reset" {
				if err := a.Reset(ctx); err != nil {
					_ = broker.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: in.Channel,
						ChatID:  in.ChatID,
						Text:    "reset error: " + err.Error(),
					})
					continue
				}
				_ = broker.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: in.Channel,
					ChatID:  in.ChatID,
					Text:    "history reset",
				})
				continue
			}

			answer, err := a.Reply(ctx, text)
			if err != nil {
				answer = "reply error: " + err.Error()
			}
			if err := broker.PublishOutbound(ctx, bus.OutboundMessage{
				Channel: in.Channel,
				ChatID:  in.ChatID,
				Text:    answer,
			}); err != nil {
				return err
			}
		}
	}
}

func runTelegramSender(ctx context.Context, broker *bus.Broker, channel *telegram.Channel) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case out := <-broker.Outbound():
			if out.Channel != bus.ChannelTelegram {
				continue
			}

			if err := channel.SendText(ctx, out.ChatID, out.Text); err != nil {
				fmt.Fprintf(os.Stderr, "telegram send error: %v\n", err)
			}
		}
	}
}
