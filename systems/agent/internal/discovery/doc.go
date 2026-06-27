// Package discovery orchestrates live model-roster discovery and models.dev
// enrichment.
//
// Provider packages own provider-specific roster protocol adapters (for
// example Ollama /api/tags and OpenAI-compatible /v1/models). This package is
// the catalog composition layer: it dispatches to those adapters, enriches the
// returned roster with models.dev metadata, and applies include/exclude filters.
// Neutral catalog types and helpers live in internal/modelcatalog so provider
// adapters do not import this orchestration package.
//
// Discovery degrades gracefully: per-provider HTTP failures are returned as
// errors (the caller skips the provider), models.dev failures are silent
// (models stay usable without enrichment), and provider-level best-effort
// details such as Ollama /api/show failures fall back inside the provider
// adapter.
package discovery
