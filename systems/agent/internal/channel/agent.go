// Package channel defines transport-facing ports used by the app worker.
package channel

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
)

// AgentEndpoint adapts one chat transport to the generic app worker.
type AgentEndpoint interface {
	Channel() string
	OpenSession(ctx context.Context, msg bus.InboundMessage) (AgentSession, error)
}

// OutboundEndpoint delivers unsolicited transport-bound messages.
type OutboundEndpoint interface {
	Channel() string
	Deliver(ctx context.Context, msg bus.OutboundMessage) error
}

// AgentSession owns transport-specific run UX for one inbound message.
type AgentSession interface {
	agent.RunObserver
	Finish(ctx context.Context, result agent.ReplyResult)
	Abort(ctx context.Context, reason string)
}

// CancellableAgentSession optionally connects transport stop controls to the
// current run. The worker calls SetCancel before forwarding any run events.
type CancellableAgentSession interface {
	AgentSession
	SetCancel(context.CancelFunc)
}
