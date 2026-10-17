package embedtools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/embed"
)

func TestSourcesAddDefaultsEnabledAndReturnsStableJSON(t *testing.T) {
	service := &fakeService{}
	tool := NewSources(service)

	got, err := tool.Run(context.Background(), `{
		"action": "add",
		"id": "library/book",
		"collection": "library",
		"source_type": "chunked_markdown_tree",
		"path": "/workspace/library/author/book/chunks"
	}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !service.added.Enabled {
		t.Fatal("added source Enabled = false, want true")
	}
	for _, want := range []string{
		`"id": "library/book"`,
		`"collection": "library"`,
		`"source_type": "chunked_markdown_tree"`,
		`"enabled": true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSourcesAddRequiresTypedSourceFields(t *testing.T) {
	tool := NewSources(&fakeService{})
	_, err := tool.Run(context.Background(), `{"action":"add","collection":"library"}`)
	if err == nil ||
		!strings.Contains(err.Error(), "missing required argument for add: source_type") {
		t.Fatalf("Run() error = %v, want missing source_type", err)
	}
}

func TestSourcesDeleteCollectionReturnsResetSummary(t *testing.T) {
	service := &fakeService{}
	tool := NewSources(service)

	got, err := tool.Run(context.Background(), `{
		"action": "delete_collection",
		"collection": "semantic"
	}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if service.deletedCollection != embed.CollectionSemantic {
		t.Fatalf("deleted collection = %q, want semantic", service.deletedCollection)
	}
	for _, want := range []string{
		`"collection": "semantic"`,
		`"deleted": true`,
		`"state_points": 2`,
		`"state_sync_runs": 1`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSearchReturnsServiceResultsAsJSON(t *testing.T) {
	tool := NewSearch(&fakeService{
		results: []embed.SearchResult{
			{
				Collection: "semantic",
				ID:         "point-1",
				Score:      0.9,
				Payload:    map[string]any{"source_id": "docs"},
			},
		},
	})
	got, err := tool.Run(
		context.Background(),
		`{"query":"hello","collection":"semantic","limit":1}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		`"collection": "semantic"`,
		`"id": "point-1"`,
		`"source_id": "docs"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSyncWaitZeroSecondsReturnsImmediateJobSnapshot(t *testing.T) {
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
			Progress: embed.SyncProgress{
				SourceID:     "docs",
				SourcesTotal: 1,
			},
		},
	}
	tool := NewSync(jobs)

	got, err := tool.Run(
		context.Background(),
		`{"wait_seconds":0,"collection":"semantic","source_id":"docs"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if jobs.getCalls != 0 {
		t.Fatalf(
			"Get() calls = %d, want 0 for an immediate wait_seconds=0 return",
			jobs.getCalls,
		)
	}
	if len(jobs.startOpts) != 1 {
		t.Fatalf("Start() calls = %d, want 1", len(jobs.startOpts))
	}
	if opts := jobs.startOpts[0]; opts.Collection != "semantic" || opts.SourceID != "docs" {
		t.Fatalf("Start() opts = %#v, want collection semantic and source docs", opts)
	}
	for _, want := range []string{
		`"id": "embed-sync-1"`,
		`"status": "running"`,
		`"source_id": "docs"`,
		`"sources_total": 1`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"result"`) {
		t.Fatalf("immediate output must not include a result:\n%s", got)
	}
}

func TestSyncDefaultWaitReturnsCompletedJobWithResult(t *testing.T) {
	started := time.Now()
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:        "embed-sync-1",
			Status:    embed.SyncJobStatusRunning,
			StartedAt: started,
		},
		getJob: embed.SyncJob{
			ID:         "embed-sync-1",
			Status:     embed.SyncJobStatusCompleted,
			StartedAt:  started,
			FinishedAt: &started,
			Result: &embed.SyncResult{
				Scanned:   2,
				Embedded:  1,
				Upserted:  1,
				Unchanged: 1,
			},
		},
		getOK: true,
	}
	tool := NewSync(jobs)

	got, err := tool.Run(
		context.Background(),
		`{"collection":"semantic","source_id":"docs","full":true}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(jobs.startOpts) != 1 || !jobs.startOpts[0].Full {
		t.Fatalf("Start() opts = %#v, want full sync", jobs.startOpts)
	}
	for _, want := range []string{
		`"status": "completed"`,
		`"result"`,
		`"scanned": 2`,
		`"embedded": 1`,
		`"upserted": 1`,
		`"unchanged": 1`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSyncWaitWindowPollsUntilTerminal(t *testing.T) {
	jobs := &fakeJobs{
		runningPolls: 1,
		runningJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
		},
		getJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusCompleted,
			Result: &embed.SyncResult{Embedded: 2},
		},
		getOK: true,
	}
	tool := NewSync(jobs)

	got, err := tool.Run(context.Background(), `{"source_id":"docs"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if jobs.getCalls < 2 {
		t.Fatalf("Get() calls = %d, want polling until terminal", jobs.getCalls)
	}
	if !strings.Contains(got, `"embedded": 2`) {
		t.Fatalf("output missing final result counters:\n%s", got)
	}
}

func TestSyncCancelledWaitReturnsRunningJobWithNote(t *testing.T) {
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
		},
		getJob: embed.SyncJob{
			ID:         "embed-sync-1",
			Status:     embed.SyncJobStatusRunning,
			StartedAt:  time.Now(),
			Progress:   embed.SyncProgress{SourceID: "docs", SourcesTotal: 1},
			FinishedAt: nil,
		},
		getOK: true,
	}
	tool := NewSync(jobs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := tool.Run(ctx, `{"collection":"semantic"}`)
	if err != nil {
		t.Fatalf("Run() error = %v, want success output with a note", err)
	}
	for _, want := range []string{
		`"status": "running"`,
		`"note": "sync continues in background"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"result"`) {
		t.Fatalf("interrupted output must not include a result:\n%s", got)
	}
}

func TestSyncFailedJobReturnsErrorNamingJob(t *testing.T) {
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:     "embed-sync-7",
			Status: embed.SyncJobStatusRunning,
		},
		getJob: embed.SyncJob{
			ID:     "embed-sync-7",
			Status: embed.SyncJobStatusFailed,
			Err:    "qdrant unavailable",
		},
		getOK: true,
	}
	tool := NewSync(jobs)

	_, err := tool.Run(context.Background(), `{"source_id":"docs"}`)
	if err == nil {
		t.Fatal("Run() error = nil, want failure naming the job")
	}
	want := "sync job embed-sync-7 failed: qdrant unavailable"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Run() error = %q, want %q", err.Error(), want)
	}
}

func TestSyncCancelledJobReturnsSnapshotWithoutError(t *testing.T) {
	finished := time.Now()
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
		},
		getJob: embed.SyncJob{
			ID:         "embed-sync-1",
			Status:     embed.SyncJobStatusCancelled,
			FinishedAt: &finished,
		},
		getOK: true,
	}
	tool := NewSync(jobs)

	got, err := tool.Run(context.Background(), `{"source_id":"docs"}`)
	if err != nil {
		t.Fatalf("Run() error = %v, want cancelled snapshot without error", err)
	}
	if !strings.Contains(got, `"status": "cancelled"`) {
		t.Fatalf("output missing cancelled status:\n%s", got)
	}
	if strings.Contains(got, `"result"`) {
		t.Fatalf("cancelled output must not include a result:\n%s", got)
	}
}

func TestSyncRejectsStartWhileAnotherSyncRuns(t *testing.T) {
	jobs := &fakeJobs{
		startErr: fmt.Errorf("sync already running (job embed-sync-1)"),
	}
	tool := NewSync(jobs)

	_, err := tool.Run(context.Background(), `{"wait_seconds":0}`)
	if err == nil ||
		!strings.Contains(err.Error(), "sync already running (job embed-sync-1)") {
		t.Fatalf("Run() error = %v, want single-flight manager error", err)
	}
}

func TestSyncUnconfiguredToolFails(t *testing.T) {
	tool := NewSync(nil)
	_, err := tool.Run(context.Background(), `{"wait_seconds":0}`)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Run() error = %v, want not-configured error", err)
	}
}

func TestSyncWaitWindowElapsesReturnsRunningJobWithNote(t *testing.T) {
	jobs := &fakeJobs{
		startJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
		},
		getJob: embed.SyncJob{
			ID:        "embed-sync-1",
			Status:    embed.SyncJobStatusRunning,
			StartedAt: time.Now(),
			Progress:  embed.SyncProgress{SourceID: "docs", SourcesTotal: 1},
		},
		getOK: true,
	}
	tool := NewSync(jobs)

	got, err := tool.Run(context.Background(), `{"wait_seconds":1}`)
	if err != nil {
		t.Fatalf("Run() error = %v, want success output with a note", err)
	}
	for _, want := range []string{
		`"status": "running"`,
		`"note": "sync continues in background"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"result"`) {
		t.Fatalf("elapsed-window output must not include a result:\n%s", got)
	}
}

func TestSyncWaitSecondsOutOfRangeFails(t *testing.T) {
	jobs := &fakeJobs{}
	tool := NewSync(jobs)

	for _, waitSeconds := range []int{-1, maxSyncWaitSeconds + 1} {
		_, err := tool.Run(
			context.Background(),
			fmt.Sprintf(`{"wait_seconds":%d}`, waitSeconds),
		)
		if err == nil || !strings.Contains(err.Error(), "wait_seconds must be between 0 and 300") {
			t.Fatalf(
				"Run() with wait_seconds %d error = %v, want range error",
				waitSeconds,
				err,
			)
		}
	}
	if len(jobs.startOpts) != 0 {
		t.Fatalf("Start() calls = %d, want 0 for invalid wait windows", len(jobs.startOpts))
	}
}

func TestJobStatusDefaultsToActiveJob(t *testing.T) {
	jobs := &fakeJobs{
		activeJob: embed.SyncJob{
			ID:     "embed-sync-2",
			Status: embed.SyncJobStatusRunning,
			Progress: embed.SyncProgress{
				SourceID:     "docs",
				SourcesTotal: 1,
			},
		},
		activeOK: true,
	}
	tool := NewJob(jobs)

	got, err := tool.Run(context.Background(), `{"action":"status"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		`"id": "embed-sync-2"`,
		`"status": "running"`,
		`"source_id": "docs"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestJobStatusWithoutActiveJobFails(t *testing.T) {
	tool := NewJob(&fakeJobs{})
	_, err := tool.Run(context.Background(), `{"action":"status"}`)
	if err == nil || !strings.Contains(err.Error(), "no active sync job") {
		t.Fatalf("Run() error = %v, want no active sync job", err)
	}
}

func TestJobStatusByIDReturnsTrackedJob(t *testing.T) {
	jobs := &fakeJobs{
		getJob: embed.SyncJob{
			ID:     "embed-sync-3",
			Status: embed.SyncJobStatusCompleted,
		},
		getOK: true,
	}
	tool := NewJob(jobs)

	got, err := tool.Run(
		context.Background(),
		`{"action":"status","job_id":"embed-sync-3"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{`"id": "embed-sync-3"`, `"status": "completed"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestJobStatusUnknownIDFails(t *testing.T) {
	tool := NewJob(&fakeJobs{})
	_, err := tool.Run(
		context.Background(),
		`{"action":"status","job_id":"embed-sync-9"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "unknown sync job") {
		t.Fatalf("Run() error = %v, want unknown sync job", err)
	}
}

func TestJobCancelForwardsToManager(t *testing.T) {
	jobs := &fakeJobs{
		cancelJob: embed.SyncJob{
			ID:     "embed-sync-1",
			Status: embed.SyncJobStatusRunning,
		},
	}
	tool := NewJob(jobs)

	got, err := tool.Run(
		context.Background(),
		`{"action":"cancel","job_id":"embed-sync-1"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(jobs.cancelCalls) != 1 || jobs.cancelCalls[0] != "embed-sync-1" {
		t.Fatalf("Cancel() calls = %#v, want one call for embed-sync-1", jobs.cancelCalls)
	}
	for _, want := range []string{`"id": "embed-sync-1"`, `"status": "running"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestJobCancelRequiresJobID(t *testing.T) {
	tool := NewJob(&fakeJobs{})
	_, err := tool.Run(context.Background(), `{"action":"cancel"}`)
	if err == nil ||
		!strings.Contains(err.Error(), "missing required argument for cancel: job_id") {
		t.Fatalf("Run() error = %v, want missing job_id", err)
	}
}

func TestJobCancelErrorPropagates(t *testing.T) {
	jobs := &fakeJobs{
		cancelErr: fmt.Errorf(
			"sync job embed-sync-1 is not running (status completed)",
		),
	}
	tool := NewJob(jobs)

	_, err := tool.Run(
		context.Background(),
		`{"action":"cancel","job_id":"embed-sync-1"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("Run() error = %v, want manager cancel error", err)
	}
}

func TestJobRejectsUnsupportedAction(t *testing.T) {
	tool := NewJob(&fakeJobs{})
	_, err := tool.Run(context.Background(), `{"action":"restart"}`)
	if err == nil ||
		!strings.Contains(err.Error(), `action "restart" is not supported`) {
		t.Fatalf("Run() error = %v, want unsupported action error", err)
	}
}

func TestJobUnconfiguredToolFails(t *testing.T) {
	tool := NewJob(nil)
	_, err := tool.Run(context.Background(), `{"action":"status"}`)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Run() error = %v, want not-configured error", err)
	}
}

// --- tool test doubles ---

type fakeService struct {
	added             embed.Source
	deletedCollection string
	results           []embed.SearchResult
}

func (f *fakeService) ListSources(ctx context.Context) ([]embed.Source, error) {
	_ = ctx
	return nil, nil
}

func (f *fakeService) AddSource(
	ctx context.Context,
	source embed.Source,
) (embed.Source, error) {
	_ = ctx
	f.added = source
	return source, nil
}

func (f *fakeService) RemoveSource(ctx context.Context, id string) (embed.Source, error) {
	_, _ = ctx, id
	return embed.Source{}, nil
}

func (f *fakeService) SetSourceEnabled(
	ctx context.Context,
	id string,
	enabled bool,
) (embed.Source, error) {
	_, _ = ctx, id
	return embed.Source{Enabled: enabled}, nil
}

func (f *fakeService) DeleteCollection(
	ctx context.Context,
	collection string,
) (embed.CollectionDeleteResult, error) {
	_ = ctx
	f.deletedCollection = collection
	return embed.CollectionDeleteResult{
		Collection:    collection,
		Deleted:       true,
		StatePoints:   2,
		StateSyncRuns: 1,
	}, nil
}

func (f *fakeService) Search(
	ctx context.Context,
	opts embed.SearchOptions,
) ([]embed.SearchResult, error) {
	_, _ = ctx, opts
	return f.results, nil
}

func (f *fakeService) Status(ctx context.Context, collection string) (embed.Status, error) {
	_, _ = ctx, collection
	return embed.Status{}, nil
}

// fakeJobs is a static syncJobs double: Start records options and returns
// startJob (or startErr), Get returns runningJob for the first runningPolls
// calls and getJob afterwards, Active returns activeJob when activeOK, and
// Cancel records ids and returns cancelJob (or cancelErr).
type fakeJobs struct {
	startErr     error
	startOpts    []embed.SyncOptions
	startJob     embed.SyncJob
	getCalls     int
	runningPolls int
	runningJob   embed.SyncJob
	getJob       embed.SyncJob
	getOK        bool
	activeJob    embed.SyncJob
	activeOK     bool
	cancelErr    error
	cancelJob    embed.SyncJob
	cancelCalls  []string
}

func (f *fakeJobs) Start(
	ctx context.Context,
	opts embed.SyncOptions,
) (embed.SyncJob, error) {
	_ = ctx
	f.startOpts = append(f.startOpts, opts)
	if f.startErr != nil {
		return embed.SyncJob{}, f.startErr
	}
	return f.startJob, nil
}

func (f *fakeJobs) Get(id string) (embed.SyncJob, bool) {
	_ = id
	f.getCalls++
	if f.getCalls <= f.runningPolls {
		return f.runningJob, true
	}
	return f.getJob, f.getOK
}

func (f *fakeJobs) Active() (embed.SyncJob, bool) {
	return f.activeJob, f.activeOK
}

func (f *fakeJobs) Cancel(id string) (embed.SyncJob, error) {
	f.cancelCalls = append(f.cancelCalls, id)
	if f.cancelErr != nil {
		return embed.SyncJob{}, f.cancelErr
	}
	return f.cancelJob, nil
}

func TestSearchRunOutputByteLimit(t *testing.T) {
	const outputLimit = 16 * 1024
	result := embed.SearchResult{
		Collection: embed.CollectionSemantic,
		ID:         "synthetic-point",
		Score:      1,
		Payload:    map[string]any{"text": ""},
	}
	empty, err := json.MarshalIndent(
		map[string]any{"results": []embed.SearchResult{result}},
		"",
		"  ",
	)
	if err != nil {
		t.Fatal(err)
	}
	room := outputLimit - len(empty)
	for _, tc := range []struct {
		name, text string
		wantBytes  int
		wantError  bool
	}{
		{name: "below limit", text: strings.Repeat("x", room-1), wantBytes: outputLimit - 1},
		{name: "exact limit", text: strings.Repeat("x", room), wantBytes: outputLimit},
		{name: "over limit", text: strings.Repeat("x", room+1), wantBytes: outputLimit + 1, wantError: true},
		{name: "JSON escaping exceeds limit", text: strings.Repeat("<", room/6+1), wantBytes: len(empty) + (room/6+1)*6, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result.Payload = map[string]any{"text": tc.text}
			results := []embed.SearchResult{result}
			expected, err := json.MarshalIndent(map[string]any{"results": results}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if len(expected) != tc.wantBytes {
				t.Fatalf("fixture is %d bytes, want %d", len(expected), tc.wantBytes)
			}
			got, err := NewSearch(
				&fakeService{results: results},
			).Run(context.Background(), `{"query":"synthetic","collection":"semantic","limit":1}`)
			if tc.wantError {
				if err == nil || got != "" {
					t.Fatalf("oversized search returned %d bytes with error %v", len(got), err)
				}
				if len(err.Error()) >= 256 || !strings.Contains(err.Error(), "reduce limit") ||
					!strings.Contains(err.Error(), "read_file") {
					t.Fatalf("oversized search error is not bounded and actionable: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != string(expected) {
				t.Fatal("within-limit result JSON changed")
			}
		})
	}
}

type searchOutputVectorStore struct {
	embed.VectorStore
	results    []embed.SearchResult
	collection string
	request    embed.SearchRequest
}

func (v *searchOutputVectorStore) Search(
	_ context.Context,
	collection string,
	request embed.SearchRequest,
) ([]embed.SearchResult, error) {
	v.collection = collection
	v.request = request
	return v.results, nil
}

func TestSearchRunWithinLimitPreservesResultsAndRequest(t *testing.T) {
	vectors := &searchOutputVectorStore{results: []embed.SearchResult{
		{
			Collection: embed.CollectionSemantic,
			ID:         "synthetic-first",
			Score:      0.9,
			Payload: map[string]any{
				"text":      "A synthetic \"quoted\" note (draft) names `sample.md`.",
				"file_path": "/memory/semantic/synthetic.md",
				"section":   "Notes",
			},
		},
		{
			Collection: embed.CollectionSemantic,
			ID:         "synthetic-second",
			Score:      0.5,
			Payload: map[string]any{
				"text":     "Another synthetic note.",
				"metadata": map[string]any{"active": true},
			},
		},
	}}
	service := embed.NewService(embed.Settings{}, nil, vectors, nil)
	got, err := NewSearch(
		service,
	).Run(context.Background(), `{"query":"synthetic query","collection":"semantic","mode":"sparse","limit":2,"filter":{"source_id":"synthetic-source","section":"Notes"}}`)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.MarshalIndent(map[string]any{"results": vectors.results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(expected) {
		t.Fatal("within-limit result JSON or payload metadata changed")
	}
	wantRequest := embed.SearchRequest{
		SparseText: "synthetic query",
		Filter:     map[string]any{"source_id": "synthetic-source", "section": "Notes"},
		Mode:       embed.SearchModeSparse,
		Limit:      2,
	}
	if vectors.collection != embed.CollectionSemantic ||
		!reflect.DeepEqual(vectors.request, wantRequest) {
		t.Fatalf(
			"search request = %s %#v, want semantic %#v",
			vectors.collection,
			vectors.request,
			wantRequest,
		)
	}
}

type searchOutputModel struct{ requests [][]conversation.Message }

func (m *searchOutputModel) Complete(
	_ context.Context,
	_ string,
	messages []conversation.Message,
	_ []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	m.requests = append(m.requests, conversation.CloneMessages(messages))
	if len(m.requests) == 1 {
		return agent.ModelClientResult{
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"synthetic-search",
						"embed_search",
						`{"query":"synthetic","collection":"semantic","mode":"sparse","limit":1}`,
					),
				),
			},
		}, nil
	}
	return agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text("Search complete.", "")),
		},
	}, nil
}

func TestSearchRunOversizedResultStaysOutOfNextModelRequest(t *testing.T) {
	const marker = "synthetic-oversized-hit:"
	vectors := &searchOutputVectorStore{
		results: []embed.SearchResult{
			{
				Collection: embed.CollectionSemantic,
				ID:         "synthetic-point",
				Score:      1,
				Payload: map[string]any{
					"text":      marker + strings.Repeat("x", 4*1024*1024),
					"file_path": "/memory/semantic/synthetic.md",
				},
			},
		},
	}
	service := embed.NewService(embed.Settings{}, nil, vectors, nil)
	registry, err := agent.NewToolRegistry(NewSearch(service))
	if err != nil {
		t.Fatal(err)
	}
	model := &searchOutputModel{}
	_, err = agent.NewEngine(model, registry, []string{"synthetic"}).
		Run(context.Background(), agent.EngineRequest{Messages: []conversation.Message{conversation.SystemMessage("Maintain synthetic memory.")}, UseTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(model.requests))
	}
	var toolResults []conversation.Part
	for _, message := range model.requests[1] {
		for _, part := range message.Parts {
			if strings.Contains(part.Text+part.Content+part.Arguments, marker) {
				t.Fatal("oversized search payload reached the next model request")
			}
			if part.Type == conversation.ToolResultPartType {
				toolResults = append(toolResults, part)
			}
		}
	}
	if len(toolResults) != 1 {
		t.Fatalf("tool results = %d, want 1", len(toolResults))
	}
	result := toolResults[0]
	if !result.IsError || result.ToolCallID != "synthetic-search" || len(result.Content) >= 256 ||
		!strings.Contains(result.Content, "read_file") {
		t.Fatalf("tool error = %#v, want bounded error with lookup guidance", result)
	}
	if vectors.request.Limit != 1 {
		t.Fatalf("search request limit = %d, want 1", vectors.request.Limit)
	}
}
