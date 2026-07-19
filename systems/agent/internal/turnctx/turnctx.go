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

// WithOrigin returns a child context carrying origin.
func WithOrigin(ctx context.Context, origin Origin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom returns the turn origin carried by ctx.
func OriginFrom(ctx context.Context) (Origin, bool) {
	origin, ok := ctx.Value(originKey{}).(Origin)
	return origin, ok
}
