// Package embedtools exposes typed embedding source and search tools.
package embedtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/embed"
)

type service interface {
	ListSources(ctx context.Context) ([]embed.Source, error)
	AddSource(ctx context.Context, source embed.Source) (embed.Source, error)
	RemoveSource(ctx context.Context, id string) (embed.Source, error)
	SetSourceEnabled(ctx context.Context, id string, enabled bool) (embed.Source, error)
	Sync(ctx context.Context, opts embed.SyncOptions) (embed.SyncResult, error)
	Search(ctx context.Context, opts embed.SearchOptions) ([]embed.SearchResult, error)
	Status(ctx context.Context, collection string) (embed.Status, error)
}

// Sources manages the typed embedding source registry.
type Sources struct {
	service service
}

// Sync runs embedding synchronization.
type Sync struct {
	service service
}

// Search runs dense semantic embedding search.
type Search struct {
	service service
}

// Status reports embedding source and collection health.
type Status struct {
	service service
}

// NewSources constructs the source-registry embedding tool.
func NewSources(service service) *Sources {
	return &Sources{service: service}
}

// NewSync constructs the embedding sync tool.
func NewSync(service service) *Sync {
	return &Sync{service: service}
}

// NewSearch constructs the embedding search tool.
func NewSearch(service service) *Search {
	return &Search{service: service}
}

// NewStatus constructs the embedding status tool.
func NewStatus(service service) *Status {
	return &Status{service: service}
}

// Definition returns the embed_sources tool schema.
func (s *Sources) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_sources",
		Description: "List, add, remove, enable, and disable typed embedding ingestion sources.",
		PromptGuidance: []string{
			"Use source_type to choose scanner behavior; never infer behavior from the collection name.",
			"Use chunked_markdown_tree for pre-chunked Markdown corpora such as /workspace/library/<author>/<work>/chunks.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Source registry action",
					"enum":        []string{"list", "add", "remove", "enable", "disable"},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Stable source identifier; required for remove, enable, and disable; optional for add",
				},
				"collection": map[string]any{
					"type":        "string",
					"description": "Target Qdrant collection for add",
					"enum":        embed.SupportedCollections(),
				},
				"source_type": map[string]any{
					"type":        "string",
					"description": "Typed scanner/parser behavior for add",
					"enum":        embed.SupportedSourceTypes(),
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Runtime path under /workspace, /memory, or /skills",
				},
				"include_globs": map[string]any{
					"type":        "array",
					"description": "Optional include globs relative to path",
					"items":       map[string]any{"type": "string"},
				},
				"exclude_globs": map[string]any{
					"type":        "array",
					"description": "Optional exclude globs relative to path",
					"items":       map[string]any{"type": "string"},
				},
				"metadata_path": map[string]any{
					"type":        "string",
					"description": "Optional runtime metadata file path for chunked_markdown_tree sources",
				},
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "Optional add-time enabled state; defaults to true",
				},
			},
			"required": []string{"action"},
		},
	}
}

// Run executes an embed_sources action.
func (s *Sources) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.service == nil {
		return "", fmt.Errorf("embedding source tool is not configured")
	}
	var args struct {
		Action       string   `json:"action"`
		ID           string   `json:"id"`
		Collection   string   `json:"collection"`
		SourceType   string   `json:"source_type"`
		Path         string   `json:"path"`
		IncludeGlobs []string `json:"include_globs"`
		ExcludeGlobs []string `json:"exclude_globs"`
		MetadataPath string   `json:"metadata_path"`
		Enabled      *bool    `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "list":
		sources, err := s.service.ListSources(ctx)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"sources": sources})
	case "add":
		if strings.TrimSpace(args.Collection) == "" {
			return "", fmt.Errorf("missing required argument for add: collection")
		}
		if strings.TrimSpace(args.SourceType) == "" {
			return "", fmt.Errorf("missing required argument for add: source_type")
		}
		if strings.TrimSpace(args.Path) == "" {
			return "", fmt.Errorf("missing required argument for add: path")
		}
		enabled := true
		if args.Enabled != nil {
			enabled = *args.Enabled
		}
		source, err := s.service.AddSource(ctx, embed.Source{
			ID:           args.ID,
			Collection:   args.Collection,
			SourceType:   args.SourceType,
			Path:         args.Path,
			IncludeGlobs: args.IncludeGlobs,
			ExcludeGlobs: args.ExcludeGlobs,
			MetadataPath: args.MetadataPath,
			Enabled:      enabled,
		})
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"source": source})
	case "remove":
		source, err := s.service.RemoveSource(ctx, args.ID)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"removed": source})
	case "enable":
		source, err := s.service.SetSourceEnabled(ctx, args.ID, true)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"source": source})
	case "disable":
		source, err := s.service.SetSourceEnabled(ctx, args.ID, false)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"source": source})
	default:
		return "", fmt.Errorf(
			"action %q is not supported (want list, add, remove, enable, or disable)",
			args.Action,
		)
	}
}

// Definition returns the embed_sync tool schema.
func (s *Sync) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_sync",
		Description: "Synchronize enabled typed embedding sources into Qdrant, including dirty upserts and pruning.",
		PromptGuidance: []string{
			"Run after adding, removing, enabling, or disabling embedding sources.",
			"Use source_id or collection only to narrow a sync; parser behavior still comes from each source_type.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"collection": map[string]any{
					"type":        "string",
					"description": "Optional target collection to sync",
					"enum":        embed.SupportedCollections(),
				},
				"source_id": map[string]any{
					"type":        "string",
					"description": "Optional source identifier to sync",
				},
				"full": map[string]any{
					"type":        "boolean",
					"description": "When true, re-embed changed and unchanged documents",
				},
			},
		},
	}
}

// Run executes an embed_sync request.
func (s *Sync) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.service == nil {
		return "", fmt.Errorf("embedding sync tool is not configured")
	}
	var args struct {
		Collection string `json:"collection"`
		SourceID   string `json:"source_id"`
		Full       bool   `json:"full"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	result, err := s.service.Sync(ctx, embed.SyncOptions{
		Collection: args.Collection,
		SourceID:   args.SourceID,
		Full:       args.Full,
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result)
}

// Definition returns the embed_search tool schema.
func (s *Search) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_search",
		Description: "Run semantic, lexical, or hybrid search over one or more Qdrant embedding collections.",
		PromptGuidance: []string{
			"Use for local semantic recall from configured embedding sources.",
			"Omit mode for hybrid dense+sparse search. Use dense to force Gemini-only semantic search or sparse to avoid an embedding call.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query text",
				},
				"collection": map[string]any{
					"type":        "string",
					"description": "Optional collection to search; omit to search all supported collections",
					"enum":        embed.SupportedCollections(),
				},
				"filter": map[string]any{
					"type":        "object",
					"description": "Optional exact payload filters",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "Search mode",
					"enum":        []string{"dense", "sparse", "hybrid"},
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum results to return, clamped to 50",
					"minimum":     1,
					"maximum":     50,
				},
			},
			"required": []string{"query"},
		},
	}
}

// Run executes an embed_search request.
func (s *Search) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.service == nil {
		return "", fmt.Errorf("embedding search tool is not configured")
	}
	var args struct {
		Query      string         `json:"query"`
		Collection string         `json:"collection"`
		Filter     map[string]any `json:"filter"`
		Mode       string         `json:"mode"`
		Limit      int            `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	results, err := s.service.Search(ctx, embed.SearchOptions{
		Query:      args.Query,
		Collection: args.Collection,
		Filter:     args.Filter,
		Mode:       args.Mode,
		Limit:      args.Limit,
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(map[string]any{"results": results})
}

// Definition returns the embed_status tool schema.
func (s *Status) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_status",
		Description: "Report embedding source state and Qdrant collection health.",
		PromptGuidance: []string{
			"Use before sync/search to inspect configured typed sources and collection point counts.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"collection": map[string]any{
					"type":        "string",
					"description": "Optional collection to inspect",
					"enum":        embed.SupportedCollections(),
				},
			},
		},
	}
}

// Run executes an embed_status request.
func (s *Status) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.service == nil {
		return "", fmt.Errorf("embedding status tool is not configured")
	}
	var args struct {
		Collection string `json:"collection"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments JSON: %w", err)
		}
	}
	status, err := s.service.Status(ctx, args.Collection)
	if err != nil {
		return "", err
	}
	return jsonOutput(status)
}

func jsonOutput(value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode tool output: %w", err)
	}
	return string(data), nil
}
