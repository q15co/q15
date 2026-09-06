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
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

var agentSessionAbortTimeout = 5 * time.Second

const runtimeWorkerShutdownTimeout = 35 * time.Second

type runtimeWorker func(context.Context) error

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

			cancelCtx, cancel := context.WithCancel(ctx)
			runCtx := turnctx.WithOrigin(cancelCtx, turnctx.Origin{
				Channel:   in.Channel,
				ChatID:    in.ChatID,
				UserID:    in.UserID,
				MessageID: in.MessageID,
			})
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
			if cancellable, ok := session.(channelport.CancellableAgentSession); ok {
				cancellable.SetCancel(cancel)
			}

			reply, err := a.Reply(runCtx, userMessage, session)
			if runCtx.Err() != nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(
					context.WithoutCancel(runCtx),
					agentSessionAbortTimeout,
				)
				session.Abort(cleanupCtx, "canceled")
				cleanupCancel()
				cancel()
				if ctx.Err() != nil {
					return nil
				}
				continue
			}
			if err != nil {
				reply = agent.ReplyResult{Text: formatReplyError(err)}
			}
			session.Finish(runCtx, reply)
			cancel()
		}
	}
}

func runOutboundWorker(
	ctx context.Context,
	messageBus *bus.Bus,
	endpoints ...channelport.OutboundEndpoint,
) error {
	registry, err := buildOutboundEndpointRegistry(endpoints...)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case out := <-messageBus.Outbound():
			reportOutboundDeliveryError(out, deliverOutbound(ctx, registry, out))
		case request := <-messageBus.OutboundDeliveryRequests():
			out := request.Message()
			deliveryCtx, cleanup := outboundDeliveryContext(ctx, request.Context())
			err := deliverOutbound(deliveryCtx, registry, out)
			cleanup()
			request.Acknowledge(err)
			reportOutboundDeliveryError(out, err)
		}
	}
}

func deliverOutbound(
	ctx context.Context,
	registry map[string]channelport.OutboundEndpoint,
	out bus.OutboundMessage,
) error {
	channelName := strings.TrimSpace(out.Channel)
	endpoint, ok := registry[channelName]
	if !ok {
		return fmt.Errorf("no outbound endpoint registered for channel %q", channelName)
	}
	return endpoint.Deliver(ctx, out)
}

func outboundDeliveryContext(
	workerCtx context.Context,
	publisherCtx context.Context,
) (context.Context, func()) {
	deliveryCtx, cancel := context.WithCancel(publisherCtx)
	stopWorkerCancellation := context.AfterFunc(workerCtx, cancel)
	return deliveryCtx, func() {
		stopWorkerCancellation()
		cancel()
	}
}

func reportOutboundDeliveryError(out bus.OutboundMessage, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	fmt.Fprintf(
		os.Stderr,
		"deliver outbound message error (%s): %v\n",
		out.Channel,
		err,
	)
}

// runRuntimeWorkers waits for parent cancellation or the first worker result,
// then cancels and joins every worker. shutdownTimeout bounds the join while
// allowing the scheduler's cancellation-safe final persistence to complete.
func runRuntimeWorkers(
	ctx context.Context,
	cancel context.CancelFunc,
	shutdownTimeout time.Duration,
	workers ...runtimeWorker,
) error {
	if cancel == nil {
		return errors.New("runtime worker cancel function is required")
	}
	if len(workers) == 0 {
		cancel()
		return errors.New("at least one runtime worker is required")
	}
	if shutdownTimeout <= 0 {
		cancel()
		return errors.New("runtime worker shutdown timeout must be positive")
	}
	for i, worker := range workers {
		if worker == nil {
			cancel()
			return fmt.Errorf("runtime worker[%d] is required", i)
		}
	}

	results := make(chan error, len(workers))
	for _, worker := range workers {
		go func() {
			results <- worker(ctx)
		}()
	}

	remaining := len(workers)
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-results:
		remaining--
		runErr = joinRuntimeWorkerError(runErr, err)
	}
	cancel()

	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case err := <-results:
			remaining--
			runErr = joinRuntimeWorkerError(runErr, err)
		case <-timer.C:
			return errors.Join(
				runErr,
				fmt.Errorf(
					"runtime worker shutdown timed out after %s (%d still running)",
					shutdownTimeout,
					remaining,
				),
			)
		}
	}
	return runErr
}

func joinRuntimeWorkerError(current error, next error) error {
	if next == nil || errors.Is(next, context.Canceled) {
		return current
	}
	return errors.Join(current, next)
}

func userMessageFromInbound(in bus.InboundMessage) conversation.Message {
	parts := make([]conversation.Part, 0, 1+len(in.Attachments))

	if text := strings.TrimSpace(in.Text); text != "" {
		parts = append(parts, conversation.Text(text, ""))
	}
	parts = append(parts, conversation.NormalizeParts(in.Attachments)...)

	sentAt := in.SentAt
	if sentAt.IsZero() {
		sentAt = time.Now().In(time.Local)
	}
	sentAt = sentAt.In(time.Local)

	return conversation.Message{
		Role:  conversation.UserRole,
		Parts: conversation.CloneParts(parts),
		UserTemporal: &conversation.UserTemporalMetadata{
			TimeLocal: sentAt,
		},
	}
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

func buildOutboundEndpointRegistry(
	endpoints ...channelport.OutboundEndpoint,
) (map[string]channelport.OutboundEndpoint, error) {
	registry := make(map[string]channelport.OutboundEndpoint, len(endpoints))

	for _, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		channelName := strings.TrimSpace(endpoint.Channel())
		if channelName == "" {
			return nil, errors.New("outbound channel endpoint name is required")
		}
		if _, exists := registry[channelName]; exists {
			return nil, fmt.Errorf("duplicate outbound channel endpoint %q", channelName)
		}
		registry[channelName] = endpoint
	}

	if len(registry) == 0 {
		return nil, errors.New("at least one outbound channel endpoint is required")
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
