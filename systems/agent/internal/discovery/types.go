package discovery

import "github.com/q15co/q15/systems/agent/internal/modelcatalog"

// Capabilities is an alias for the neutral modelcatalog capability set.
type Capabilities = modelcatalog.Capabilities

// Model is an alias for the neutral modelcatalog roster entry.
type Model = modelcatalog.Model

// Options is an alias for the neutral modelcatalog discovery options.
type Options = modelcatalog.Options

// Provider is an alias for the neutral modelcatalog provider descriptor.
type Provider = modelcatalog.Provider

// Catalog is an alias for the neutral modelcatalog discovery port.
type Catalog = modelcatalog.Catalog

// NoopCatalog is an alias for the neutral no-op catalog implementation.
type NoopCatalog = modelcatalog.NoopCatalog

const (
	sourceOllama    = modelcatalog.SourceOllama
	sourceOpenAI    = modelcatalog.SourceOpenAI
	sourceModelsDev = modelcatalog.SourceModelsDev
)
