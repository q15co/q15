// Package embedtools exposes typed embedding source and search tools.
package embedtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/embed"
)

type service interface {
	ListSources(ctx context.Context) ([]embed.Source, error)
	AddSource(ctx context.Context, source embed.Source) (embed.Source, error)
	RemoveSource(ctx context.Context, id string) (embed.Source, error)
	SetSourceEnabled(ctx context.Context, id string, enabled bool) (embed.Source, error)
	DeleteCollection(ctx context.Context, collection string) (embed.CollectionDeleteResult, error)
	Search(ctx context.Context, opts embed.SearchOptions) ([]embed.SearchResult, error)
	Status(ctx context.Context, collection string) (embed.Status, error)
}

// syncJobs is the managed-sync surface the embed_sync and embed_job tools
// depend on. embed.SyncJobManager implements it.
type syncJobs interface {
	Start(ctx context.Context, opts embed.SyncOptions) (embed.SyncJob, error)
	Get(id string) (embed.SyncJob, bool)
	Active() (embed.SyncJob, bool)
	Cancel(id string) (embed.SyncJob, error)
}

const (
	// syncPollInterval is how often embed_sync polls the manager while
	// waiting for a sync job to reach a terminal state.
	syncPollInterval = 250 * time.Millisecond
	// defaultSyncWaitSeconds bounds how long embed_sync blocks by default
	// before returning the still-running job snapshot.
	defaultSyncWaitSeconds = 30
	// maxSyncWaitSeconds bounds the embed_sync wait window.
	maxSyncWaitSeconds = 300
)

// Sources manages the typed embedding source registry.
type Sources struct {
	service service
}

// Sync runs embedding synchronization as a managed background job.
type Sync struct {
	jobs syncJobs
}

// Job inspects and cancels asynchronous embedding sync jobs.
type Job struct {
	jobs syncJobs
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
func NewSync(jobs syncJobs) *Sync {
	return &Sync{jobs: jobs}
}

// NewJob constructs the embed_job tool.
func NewJob(jobs syncJobs) *Job {
	return &Job{jobs: jobs}
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
		Description: "List, add, remove, enable, disable, and reset typed embedding ingestion sources and collections.",
		PromptGuidance: []string{
			"Use source_type to choose scanner behavior; never infer behavior from the collection name.",
			"Use chunked_markdown_tree for pre-chunked Markdown corpora such as /workspace/library/<author>/<work>/chunks.",
			"Use delete_collection only for deliberate collection resets; it drops the Qdrant collection and clears matching sync state.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Source registry action",
					"enum": []string{
						"list",
						"add",
						"remove",
						"enable",
						"disable",
						"delete_collection",
					},
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Stable source identifier; required for remove, enable, and disable; optional for add",
				},
				"collection": map[string]any{
					"type":        "string",
					"description": "Target Qdrant collection for add or delete_collection",
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
	case "delete_collection":
		if strings.TrimSpace(args.Collection) == "" {
			return "", fmt.Errorf("missing required argument for delete_collection: collection")
		}
		result, err := s.service.DeleteCollection(ctx, args.Collection)
		if err != nil {
			return "", err
		}
		return jsonOutput(result)
	default:
		return "", fmt.Errorf(
			"action %q is not supported (want list, add, remove, enable, disable, or delete_collection)",
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
			"The call returns a job snapshot after wait_seconds (default 30) even when the sync is still running; poll embed_job to completion before reporting results. wait_seconds 0 returns immediately.",
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
				"wait_seconds": map[string]any{
					"type":        "integer",
					"default":     30,
					"minimum":     0,
					"maximum":     300,
					"description": "How long to block waiting for the sync to finish. 0 returns the job snapshot immediately; if the window elapses first, the still-running job is returned and embed_job polls or cancels it.",
				},
			},
		},
	}
}

// Run executes an embed_sync request. The sync always runs as a managed
// background job on a context detached from this run; wait_seconds (default
// 30) bounds how long the call blocks before returning the still-running
// job snapshot, and 0 returns the snapshot immediately.
func (s *Sync) Run(ctx context.Context, arguments string) (string, error) {
	if s == nil || s.jobs == nil {
		return "", fmt.Errorf("embedding sync tool is not configured")
	}
	var args struct {
		Collection  string `json:"collection"`
		SourceID    string `json:"source_id"`
		Full        bool   `json:"full"`
		WaitSeconds *int   `json:"wait_seconds"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	waitSeconds := defaultSyncWaitSeconds
	if args.WaitSeconds != nil {
		waitSeconds = *args.WaitSeconds
	}
	if waitSeconds < 0 || waitSeconds > maxSyncWaitSeconds {
		return "", fmt.Errorf(
			"wait_seconds must be between 0 and %d",
			maxSyncWaitSeconds,
		)
	}
	job, err := s.jobs.Start(ctx, embed.SyncOptions{
		Collection: args.Collection,
		SourceID:   args.SourceID,
		Full:       args.Full,
	})
	if err != nil {
		return "", err
	}
	if waitSeconds == 0 {
		return jsonOutput(map[string]any{"job": job})
	}
	return s.awaitSyncJob(ctx, job.ID, waitSeconds)
}

// awaitSyncJob polls the manager every syncPollInterval until the job
// reaches a terminal state or the wait window elapses. When the run context
// is cancelled or the window elapses while the sync is still running, the
// sync keeps running in the background and the tool reports the running
// snapshot with a note instead of an error.
func (s *Sync) awaitSyncJob(
	ctx context.Context,
	id string,
	waitSeconds int,
) (string, error) {
	ticker := time.NewTicker(syncPollInterval)
	defer ticker.Stop()
	window := time.NewTimer(time.Duration(waitSeconds) * time.Second)
	defer window.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.runningSnapshot(id)
		case <-window.C:
			return s.runningSnapshot(id)
		case <-ticker.C:
			job, ok := s.jobs.Get(id)
			if !ok {
				return "", fmt.Errorf("sync job %q not found", id)
			}
			if syncJobTerminal(job.Status) {
				return syncJobOutput(job)
			}
		}
	}
}

// runningSnapshot renders the latest snapshot of a still-running job so the
// caller can re-attach with embed_job instead of blocking this run.
func (s *Sync) runningSnapshot(id string) (string, error) {
	job, ok := s.jobs.Get(id)
	if !ok {
		return "", fmt.Errorf("sync job %q not found", id)
	}
	return jsonOutput(map[string]any{
		"job":  job,
		"note": "sync continues in background",
	})
}

// syncJobTerminal reports whether one sync job status is final.
func syncJobTerminal(status string) bool {
	switch status {
	case embed.SyncJobStatusCompleted, embed.SyncJobStatusFailed, embed.SyncJobStatusCancelled:
		return true
	default:
		return false
	}
}

// syncJobOutput renders one terminal sync job: completed jobs include the
// final sync result, cancelled jobs report the snapshot without an error,
// and failed jobs surface an error string naming the job.
func syncJobOutput(job embed.SyncJob) (string, error) {
	switch job.Status {
	case embed.SyncJobStatusFailed:
		return "", fmt.Errorf("sync job %s failed: %s", job.ID, job.Err)
	case embed.SyncJobStatusCancelled:
		return jsonOutput(map[string]any{"job": job})
	default:
		result := embed.SyncResult{}
		if job.Result != nil {
			result = *job.Result
		}
		return jsonOutput(map[string]any{"job": job, "result": result})
	}
}

// Definition returns the embed_job tool schema.
func (j *Job) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_job",
		Description: "Inspect or cancel asynchronous embedding sync jobs started with embed_sync.",
		PromptGuidance: []string{
			"Poll status of async embed syncs started with embed_sync (see its wait_seconds); use cancel to stop a running sync. Progress counters are cumulative across the whole sync run.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "Sync job action",
					"enum":        []string{"status", "cancel"},
				},
				"job_id": map[string]any{
					"type":        "string",
					"description": "Sync job id; required for cancel, optional for status",
				},
			},
			"required": []string{"action"},
		},
	}
}

// Run executes an embed_job action.
func (j *Job) Run(_ context.Context, arguments string) (string, error) {
	if j == nil || j.jobs == nil {
		return "", fmt.Errorf("embedding job tool is not configured")
	}
	var args struct {
		Action string `json:"action"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "status":
		job, err := j.status(args.JobID)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"job": job})
	case "cancel":
		id := strings.TrimSpace(args.JobID)
		if id == "" {
			return "", fmt.Errorf("missing required argument for cancel: job_id")
		}
		job, err := j.jobs.Cancel(id)
		if err != nil {
			return "", err
		}
		return jsonOutput(map[string]any{"job": job})
	default:
		return "", fmt.Errorf(
			"action %q is not supported (want status or cancel)",
			args.Action,
		)
	}
}

// status resolves the job snapshot for one embed_job status action: the
// active job when no id is given, or the tracked job for an explicit id.
func (j *Job) status(id string) (embed.SyncJob, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		job, ok := j.jobs.Active()
		if !ok {
			return embed.SyncJob{}, fmt.Errorf("no active sync job")
		}
		return job, nil
	}
	job, ok := j.jobs.Get(id)
	if !ok {
		return embed.SyncJob{}, fmt.Errorf("unknown sync job %q", id)
	}
	return job, nil
}

// Definition returns the embed_search tool schema.
func (s *Search) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "embed_search",
		Description: "Run semantic, BM25 lexical, or hybrid search over one or more Qdrant embedding collections.",
		PromptGuidance: []string{
			"Use for local semantic recall from configured embedding sources.",
			"Omit mode for hybrid Gemini+dense and Qdrant BM25 sparse search. Use dense to force Gemini-only semantic search or sparse to avoid an embedding call.",
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
