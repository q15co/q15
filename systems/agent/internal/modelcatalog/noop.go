package modelcatalog

import "context"

// NoopCatalog returns no models. It is used by tests or callers that need a
// minimal Catalog implementation.
type NoopCatalog struct{}

// Discover implements Catalog by returning no models and no error.
func (NoopCatalog) Discover(_ context.Context, _ Provider) ([]Model, error) {
	return nil, nil
}
