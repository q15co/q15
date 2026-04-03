// Package app wires runtime configuration into running bot instances.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

var agentSessionAbortTimeout = 5 * time.Second

func runAgentWorker(
	ctx context.Context,
	messageBus *bus.Bus,
	a agent.Agent,
	endpoints ...channelport.AgentEndpoint,
) error {
	registry, err := buildEndpointRegistry(endpoints...)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-messageBus.Inbound():
			endpoint, ok := registry[in.Channel]
			if !ok {
				continue
			}

			userMessage := userMessageFromInbound(in)
			if len(userMessage.Parts) == 0 {
				continue
			}

			runCtx, cancel := context.WithCancel(ctx)
			session, err := endpoint.OpenSession(runCtx, in)
			if err != nil {
				cancel()
				fmt.Fprintf(os.Stderr, "open channel session error (%s): %v\n", in.Channel, err)
				continue
			}
			if session == nil {
				cancel()
				continue
			}

			answer, err := a.Reply(runCtx, userMessage, session)
			if ctx.Err() != nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(
					context.WithoutCancel(ctx),
					agentSessionAbortTimeout,
				)
				session.Abort(cleanupCtx, "canceled")
				cleanupCancel()
				cancel()
				return nil
			}
			if err != nil {
				answer = formatReplyError(err)
			}
			session.Finish(runCtx, answer)
			cancel()
		}
	}
}

func userMessageFromInbound(in bus.InboundMessage) conversation.Message {
	parts := make([]conversation.Part, 0, 1+len(in.Media))

	if text := strings.TrimSpace(in.Text); text != "" {
		parts = append(parts, conversation.Text(text, ""))
	}
	for _, ref := range in.Media {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		parts = append(parts, conversation.Image(ref, ""))
	}
	return conversation.UserMessageParts(parts...)
}

func buildEndpointRegistry(
	endpoints ...channelport.AgentEndpoint,
) (map[string]channelport.AgentEndpoint, error) {
	registry := make(map[string]channelport.AgentEndpoint, len(endpoints))

	for _, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		channelName := strings.TrimSpace(endpoint.Channel())
		if channelName == "" {
			return nil, errors.New("channel endpoint name is required")
		}
		if _, exists := registry[channelName]; exists {
			return nil, fmt.Errorf("duplicate channel endpoint %q", channelName)
		}
		registry[channelName] = endpoint
	}

	if len(registry) == 0 {
		return nil, errors.New("at least one channel endpoint is required")
	}
	return registry, nil
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
