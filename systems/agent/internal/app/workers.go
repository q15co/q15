package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
)

func runAgentWorker(
	ctx context.Context,
	messageBus *bus.Bus,
	a agent.Agent,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-messageBus.Inbound():
			text := strings.TrimSpace(in.Text)
			if text == "" {
				continue
			}

			answer, err := a.Reply(ctx, text)
			if err != nil {
				answer = formatReplyError(err)
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

func formatReplyError(err error) string {
	var stopErr *agent.StopError
	if errors.As(err, &stopErr) {
		switch stopErr.Reason {
		case agent.StopReasonToolTurnLimit:
			return "I stopped this run after reaching an internal tool-call safety limit. Progress was saved."
		case agent.StopReasonToolLoopDetected:
			return "I stopped this run because tool calls appeared stuck in a loop. Progress was saved."
		}
	}
	return "reply error: " + err.Error()
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
