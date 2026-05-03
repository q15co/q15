package lightrag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

var (
	_ agent.Tool = (*Query)(nil)
	_ agent.Tool = (*Ingest)(nil)
	_ agent.Tool = (*Status)(nil)
	_ agent.Tool = (*Graph)(nil)
)

// Query asks LightRAG for a synthesized answer with source references.
type Query struct {
	client *client
}

// Definition returns the tool schema exposed to the model.
func (q *Query) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "rag_query",
		Description: "Query the configured LightRAG knowledge graph and return a synthesized answer with source references.",
		PromptGuidance: []string{
			"Use for semantic search across the indexed local document corpus.",
			"Use mode hybrid unless you need a specific LightRAG retrieval mode.",
			"Reranking is disabled by default because the local stack does not configure a reranker; enable it only after a rerank provider is configured.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural-language question for the knowledge graph",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "LightRAG retrieval mode",
					"enum": []string{
						"local",
						"global",
						defaultQueryMode,
						"naive",
						"mix",
					},
				},
				"enable_rerank": map[string]any{
					"type":        "boolean",
					"description": "Whether LightRAG should rerank retrieved chunks. Defaults to false unless a rerank provider is configured.",
				},
			},
			"required": []string{"query"},
		},
	}
}

// Run executes one LightRAG query.
func (q *Query) Run(ctx context.Context, arguments string) (string, error) {
	if q == nil || q.client == nil {
		return "", fmt.Errorf("rag_query tool is not configured")
	}

	var args struct {
		Query        string `json:"query"`
		Mode         string `json:"mode"`
		EnableRerank *bool  `json:"enable_rerank"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("missing required argument: query")
	}
	mode, err := normalizeQueryMode(args.Mode)
	if err != nil {
		return "", err
	}
	enableRerank := false
	if args.EnableRerank != nil {
		enableRerank = *args.EnableRerank
	}

	body, err := q.client.postJSON(ctx, "/query", map[string]any{
		"query":              query,
		"mode":               mode,
		"only_need_context":  false,
		"enable_rerank":      enableRerank,
		"include_references": true,
	})
	if err != nil {
		return "", err
	}
	return renderQueryResponse(body), nil
}

// Ingest uploads a file or inserts text into LightRAG.
type Ingest struct {
	client *client
}

// Definition returns the tool schema exposed to the model.
func (i *Ingest) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "rag_ingest",
		Description: "Add a local file or raw text to the configured LightRAG knowledge index and return the ingestion track ID.",
		PromptGuidance: []string{
			"Prefer path ingestion for existing files under /workspace, /memory, or /media.",
			"When path is set it takes precedence over text and source.",
			"Use text plus source only for generated content that is not already in a file.",
			"LightRAG chunks by token windows, not by document type; for EPUBs, scanned PDFs, or layout-heavy journal PDFs, prefer preconverted Markdown/text under /workspace/library-markdown.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path under /workspace, /memory, or /media. Relative paths resolve under /workspace.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Raw text to insert when path is empty",
				},
				"source": map[string]any{
					"type":        "string",
					"description": "Source label for text ingestion; required when using text",
				},
			},
		},
	}
}

// Run executes one LightRAG ingestion request.
func (i *Ingest) Run(ctx context.Context, arguments string) (string, error) {
	if i == nil || i.client == nil {
		return "", fmt.Errorf("rag_ingest tool is not configured")
	}

	var args struct {
		Path   string `json:"path"`
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}

	if strings.TrimSpace(args.Path) != "" {
		return i.ingestFile(ctx, args.Path)
	}
	return i.ingestText(ctx, args.Text, args.Source)
}

func (i *Ingest) ingestFile(ctx context.Context, rawPath string) (string, error) {
	localPath, runtimePath, err := i.client.resolveUploadPath(rawPath)
	if err != nil {
		return "", err
	}
	file, info, err := openRegularFile(localPath, runtimePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	filename := filepath.Base(runtimePath)
	if filename == "." || filename == "/" || filename == "" {
		filename = info.Name()
	}

	body, err := i.client.postMultipartFile(ctx, "/documents/upload", "file", filename, file)
	if err != nil {
		return "", err
	}
	return renderIngestResponse(runtimePath, body), nil
}

func (i *Ingest) ingestText(ctx context.Context, text string, source string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("missing required argument: text")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("missing required argument: source")
	}

	body, err := i.client.postJSON(ctx, "/documents/text", map[string]any{
		"text":        text,
		"file_source": source,
	})
	if err != nil {
		return "", err
	}
	return renderIngestResponse(source, body), nil
}

// Status checks LightRAG pipeline or per-track ingestion status.
type Status struct {
	client *client
}

// Definition returns the tool schema exposed to the model.
func (s *Status) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "rag_status",
		Description: "Check LightRAG ingestion pipeline status, status counts, or one ingestion track ID.",
		PromptGuidance: []string{
			"Use after rag_ingest to check whether a document finished indexing.",
			"Call without track_id for pipeline-level status and document status counts.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"track_id": map[string]any{
					"type":        "string",
					"description": "Optional LightRAG ingestion track ID",
				},
			},
		},
	}
}

// Run executes one LightRAG status check.
func (s *Status) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("rag_status tool is not configured")
	}

	var args struct {
		TrackID string `json:"track_id"`
	}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments JSON: %w", err)
		}
	}
	trackID := strings.TrimSpace(args.TrackID)
	if trackID != "" {
		body, err := s.client.get(ctx, "/documents/track_status/"+url.PathEscape(trackID))
		if err != nil {
			return "", err
		}
		return "Track Status:\n" + prettyJSON(body), nil
	}

	pipeline, err := s.client.get(ctx, "/documents/pipeline_status")
	if err != nil {
		return "", err
	}
	counts, err := s.client.get(ctx, "/documents/status_counts")
	if err != nil {
		return "", err
	}
	return "Pipeline Status:\n" + prettyJSON(pipeline) +
		"\n\nDocument Status Counts:\n" + prettyJSON(counts), nil
}

// Graph searches LightRAG knowledge graph labels.
type Graph struct {
	client *client
}

// Definition returns the tool schema exposed to the model.
func (g *Graph) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "rag_graph",
		Description: "Search LightRAG knowledge graph labels to discover indexed entities and concepts.",
		PromptGuidance: []string{
			"Use before targeted rag_query calls when you need to discover available graph labels.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Label or entity text to search for",
				},
			},
			"required": []string{"query"},
		},
	}
}

// Run executes one LightRAG graph label search.
func (g *Graph) Run(ctx context.Context, arguments string) (string, error) {
	if g == nil || g.client == nil {
		return "", fmt.Errorf("rag_graph tool is not configured")
	}

	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("missing required argument: query")
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("limit", fmt.Sprintf("%d", defaultGraphLimit))
	body, err := g.client.get(ctx, "/graph/label/search?"+values.Encode())
	if err != nil {
		return "", err
	}
	return renderGraphResponse(query, body), nil
}

func normalizeQueryMode(mode string) (string, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		return defaultQueryMode, nil
	}
	switch mode {
	case "local", "global", defaultQueryMode, "naive", "mix":
		return mode, nil
	default:
		return "", fmt.Errorf(
			"mode must be one of local, global, hybrid, naive, mix",
		)
	}
}
