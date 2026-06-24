package modelcatalog

import "context"

// NoopCatalog returns no models. It is used by callers that want to disable
// discovery entirely (for example config.ResolveAgentRuntime when no provider
// enables discovery).
type NoopCatalog struct{}

// Discover implements Catalog by returning no models and no error.
func (NoopCatalog) Discover(_ context.Context, _ Provider) ([]Model, error) {
	return nil, nil
}
