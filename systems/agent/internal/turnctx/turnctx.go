// Package turnctx carries transport origin metadata through one agent turn.
package turnctx

import "context"

// Origin identifies the transport message that initiated an agent turn.
type Origin struct {
	Channel   string
	ChatID    string
	UserID    string
	MessageID string
}

type originKey struct{}

type turnSeqKey struct{}

// WithTurnSeq attaches the reserved transcript turn identity to tool dispatch.
func WithTurnSeq(ctx context.Context, seq int64) context.Context {
	return context.WithValue(ctx, turnSeqKey{}, seq)
}

// TurnSeqFrom returns zero when the caller has no reserved transcript turn.
func TurnSeqFrom(ctx context.Context) int64 {
	seq, _ := ctx.Value(turnSeqKey{}).(int64)
	return seq
}

// WithOrigin returns a child context carrying origin.
func WithOrigin(ctx context.Context, origin Origin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom returns the turn origin carried by ctx.
func OriginFrom(ctx context.Context) (Origin, bool) {
	origin, ok := ctx.Value(originKey{}).(Origin)
	return origin, ok
}
