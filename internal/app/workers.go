package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"q15.co/sandbox/internal/agent"
	"q15.co/sandbox/internal/bus"
)

func runAgentWorker(ctx context.Context, messageBus *bus.Bus, getAgent func(sessionKey string) agent.Agent) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-messageBus.Inbound():
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
					_ = messageBus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: in.Channel,
						ChatID:  in.ChatID,
						Text:    "reset error: " + err.Error(),
					})
					continue
				}
				_ = messageBus.PublishOutbound(ctx, bus.OutboundMessage{
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
			if err := messageBus.PublishOutbound(ctx, bus.OutboundMessage{
				Channel: in.Channel,
				ChatID:  in.ChatID,
				Text:    answer,
			}); err != nil {
				return err
			}
		}
	}
}

func runOutboundWorker(
	ctx context.Context,
	messageBus *bus.Bus,
	channelName string,
	send func(context.Context, string, string) error,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case out := <-messageBus.Outbound():
			if out.Channel != channelName {
				continue
			}

			if err := send(ctx, out.ChatID, out.Text); err != nil {
				fmt.Fprintf(os.Stderr, "outbound send error (%s): %v\n", channelName, err)
			}
		}
	}
}
