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

// AgentSession owns transport-specific run UX for one inbound message.
type AgentSession interface {
	agent.RunObserver
	Finish(ctx context.Context, result agent.ReplyResult)
	Abort(ctx context.Context, reason string)
}
