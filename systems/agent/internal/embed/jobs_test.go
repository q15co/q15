package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// jobSettings builds isolated runtime-local settings for job manager tests.
func jobSettings(t *testing.T) Settings {
	t.Helper()
	root := t.TempDir()
	settings := Settings{
		WorkspaceLocalDir: filepath.Join(root, "workspace"),
		MemoryLocalDir:    filepath.Join(root, "memory"),
		SkillsLocalDir:    filepath.Join(root, "skills"),
		RegistryPath:      filepath.Join(root, "registry.json"),
		StatePath:         filepath.Join(root, "state.jsonl"),
	}
	for _, dir := range []string{
		settings.WorkspaceLocalDir,
		settings.MemoryLocalDir,
		settings.SkillsLocalDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return settings
}

// jobEmbedder is a self-contained test embedder. It optionally blocks every
// EmbedDocuments call until block is closed or the context is cancelled, and
// it can fail on a configured 1-based call.
type jobEmbedder struct {
	block      <-chan struct{}
	failOnCall int
	calls      int
	mu         sync.Mutex
}

func (e *jobEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	block := e.block
	failOnCall := e.failOnCall
	e.mu.Unlock()
	if block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		}
	}
	if failOnCall > 0 && call == failOnCall {
		return nil, fmt.Errorf("job embedder failure on call %d", call)
	}
	out := make([][]float32, 0, len(reqs))
	for range reqs {
		out = append(out, []float32{1, 1})
	}
	return out, nil
}

func (e *jobEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	_, _ = ctx, text
	return []float32{1, 1}, nil
}

// jobVectorStore is a no-op vector store for job manager tests.
type jobVectorStore struct{}

func (jobVectorStore) EnsureCollection(
	ctx context.Context,
	collection string,
	dimensions int,
) (CollectionEnsureResult, error) {
	_, _, _ = ctx, collection, dimensions
	return CollectionEnsureResult{}, nil
}

func (jobVectorStore) DeleteCollection(ctx context.Context, collection string) (bool, error) {
	_, _ = ctx, collection
	return false, nil
}

func (jobVectorStore) Upsert(ctx context.Context, collection string, points []Point) error {
	_, _, _ = ctx, collection, points
	return nil
}

func (jobVectorStore) UpdatePayload(
	ctx context.Context,
	collection string,
	pointID string,
	payload map[string]any,
) error {
	_, _, _, _ = ctx, collection, pointID, payload
	return nil
}

func (jobVectorStore) Delete(ctx context.Context, collection string, pointIDs []string) error {
	_, _, _ = ctx, collection, pointIDs
	return nil
}

func (jobVectorStore) Search(
	ctx context.Context,
	collection string,
	req SearchRequest,
) ([]SearchResult, error) {
	_, _, _ = ctx, collection, req
	return nil, nil
}

func (jobVectorStore) Status(ctx context.Context, collection string) (CollectionStatus, error) {
	_, _ = ctx, collection
	return CollectionStatus{}, nil
}

func (jobVectorStore) Close() error {
	return nil
}

// newJobService builds an embedding service with one enabled markdown-tree
// source holding docs documents, ready for job manager tests.
func newJobService(t *testing.T, embedder *jobEmbedder, docs int) *Service {
	t.Helper()
	ctx := context.Background()
	settings := jobSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create docs dir: %v", err)
	}
	for i := 0; i < docs; i++ {
		path := filepath.Join(sourceDir, fmt.Sprintf("note-%d.md", i))
		body := fmt.Sprintf("note %d body\n", i)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })
	service := NewService(settings, state, jobVectorStore{}, embedder)
	if _, err := service.AddSource(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}
	return service
}

// waitForJobTerminal polls a job until it leaves the running state.
func waitForJobTerminal(t *testing.T, manager *SyncJobManager, id string) SyncJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("sync job %s not tracked", id)
		}
		if job.Status != SyncJobStatusRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync job %s still running after deadline", id)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForJobSource polls a job until its progress snapshot names sourceID as
// the source currently in flight.
func waitForJobSource(t *testing.T, manager *SyncJobManager, id, sourceID string) SyncJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("sync job %s not tracked", id)
		}
		if job.Progress.SourceID == sourceID {
			return job
		}
		if job.Status != SyncJobStatusRunning {
			t.Fatalf(
				"sync job %s ended before %q progress was visible: %#v",
				id,
				sourceID,
				job,
			)
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync job %s never reported source %q: %#v", id, sourceID, job)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newUnblock returns a close-once helper for block channels plus registers
// the close on cleanup, so fatal failures never leak blocked goroutines.
func newUnblock(t *testing.T, block chan struct{}) func() {
	t.Helper()
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			close(block)
		})
	}
	t.Cleanup(unblock)
	return unblock
}

func TestSyncJobManagerStartsDetachedJobSurvivingContextCancel(t *testing.T) {
	ctx := context.Background()
	service := newJobService(t, &jobEmbedder{}, 1)
	manager := NewSyncJobManager(service)
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()

	job, err := manager.Start(startCtx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if job.ID != "embed-sync-1" {
		t.Fatalf("job ID = %q, want embed-sync-1", job.ID)
	}
	if job.Status != SyncJobStatusRunning {
		t.Fatalf("job status = %q, want running", job.Status)
	}
	cancelStart()

	job = waitForJobTerminal(t, manager, job.ID)
	if job.Status != SyncJobStatusCompleted {
		t.Fatalf("job status = %q, want completed after start ctx cancel", job.Status)
	}
	if job.Result == nil || job.Result.Embedded != 1 {
		t.Fatalf("job result = %#v, want one embedded document", job.Result)
	}
	if _, ok := manager.Active(); ok {
		t.Fatal("Active() reported a job after completion, want none")
	}
}

func TestSyncJobManagerRejectsSecondConcurrentSync(t *testing.T) {
	ctx := context.Background()
	block := make(chan struct{})
	unblock := newUnblock(t, block)
	service := newJobService(t, &jobEmbedder{block: block}, 1)
	manager := NewSyncJobManager(service)

	first, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	if active, ok := manager.Active(); !ok || active.ID != first.ID {
		t.Fatalf("Active() = (%#v, %v), want the running job %s", active, ok, first.ID)
	}
	_, err = manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err == nil || !strings.Contains(err.Error(), "sync already running (job embed-sync-1)") {
		t.Fatalf("second Start() error = %v, want single-flight rejection", err)
	}
	unblock()
	job := waitForJobTerminal(t, manager, first.ID)
	if job.Status != SyncJobStatusCompleted {
		t.Fatalf("first job status = %q, want completed", job.Status)
	}

	second, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Start() after completion error = %v", err)
	}
	if second.ID != "embed-sync-2" {
		t.Fatalf("second job ID = %q, want embed-sync-2", second.ID)
	}
	waitForJobTerminal(t, manager, second.ID)
}

func TestSyncJobManagerCancelMarksJobCancelled(t *testing.T) {
	ctx := context.Background()
	block := make(chan struct{})
	_ = newUnblock(t, block)
	service := newJobService(t, &jobEmbedder{block: block}, 1)
	manager := NewSyncJobManager(service)

	job, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForJobSource(t, manager, job.ID, "docs")

	snapshot, err := manager.Cancel(job.ID)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if snapshot.Status != SyncJobStatusRunning {
		t.Fatalf(
			"Cancel() snapshot status = %q, want running; terminal status is set by the job goroutine",
			snapshot.Status,
		)
	}

	job = waitForJobTerminal(t, manager, job.ID)
	if job.Status != SyncJobStatusCancelled {
		t.Fatalf("job status = %q, want cancelled", job.Status)
	}
	if job.FinishedAt == nil {
		t.Fatal("cancelled job FinishedAt = nil, want set")
	}
	if !strings.Contains(job.Err, "context canceled") {
		t.Fatalf("job Err = %q, want context canceled", job.Err)
	}
	if job.Result != nil {
		t.Fatalf("cancelled job Result = %#v, want nil", job.Result)
	}
	if _, ok := manager.Active(); ok {
		t.Fatal("Active() reported a job after cancellation, want none")
	}
	if _, err := manager.Cancel(job.ID); err == nil ||
		!strings.Contains(err.Error(), "is not running") {
		t.Fatalf("Cancel() on terminal job error = %v, want not-running error", err)
	}
	if _, err := manager.Cancel("embed-sync-404"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("Cancel() on unknown job error = %v, want not-found error", err)
	}
}

func TestSyncJobManagerRecordsProgressSnapshots(t *testing.T) {
	ctx := context.Background()
	block := make(chan struct{})
	unblock := newUnblock(t, block)
	service := newJobService(t, &jobEmbedder{block: block}, 1)
	manager := NewSyncJobManager(service)

	var callerProgress []SyncProgress
	job, err := manager.Start(ctx, SyncOptions{
		SourceID: "docs",
		Progress: func(p SyncProgress) {
			callerProgress = append(callerProgress, p)
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	running := waitForJobSource(t, manager, job.ID, "docs")
	if running.Progress.SourcesTotal != 1 {
		t.Fatalf(
			"running progress SourcesTotal = %d, want 1",
			running.Progress.SourcesTotal,
		)
	}
	unblock()

	job = waitForJobTerminal(t, manager, job.ID)
	if job.Status != SyncJobStatusCompleted {
		t.Fatalf("job status = %q, want completed", job.Status)
	}
	want := SyncProgress{
		SourceID:         "",
		SourcesCompleted: 1,
		SourcesTotal:     1,
		Scanned:          1,
		Embedded:         1,
		Upserted:         1,
	}
	if job.Progress != want {
		t.Fatalf("final progress = %#v, want %#v", job.Progress, want)
	}
	if len(callerProgress) < 2 {
		t.Fatalf(
			"caller progress calls = %d, want at least source start plus one chunk",
			len(callerProgress),
		)
	}
	last := callerProgress[len(callerProgress)-1]
	if last.Scanned != 1 || last.Embedded != 1 || last.Upserted != 1 {
		t.Fatalf("last caller progress = %#v, want final chunk counters", last)
	}
}

func TestSyncJobManagerPrunesTerminalHistory(t *testing.T) {
	ctx := context.Background()
	service := newJobService(t, &jobEmbedder{}, 1)
	manager := NewSyncJobManager(service)

	for i := 0; i < maxSyncJobHistory+2; i++ {
		job, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
		if err != nil {
			t.Fatalf("Start() #%d error = %v", i+1, err)
		}
		completed := waitForJobTerminal(t, manager, job.ID)
		if completed.Status != SyncJobStatusCompleted {
			t.Fatalf("job #%d status = %q, want completed", i+1, completed.Status)
		}
	}
	if _, ok := manager.Get("embed-sync-1"); ok {
		t.Fatal("oldest job embed-sync-1 still tracked, want pruned")
	}
	if _, ok := manager.Get("embed-sync-2"); ok {
		t.Fatal("job embed-sync-2 still tracked, want pruned")
	}
	if _, ok := manager.Get("embed-sync-3"); !ok {
		t.Fatal("job embed-sync-3 missing from bounded history")
	}
	newest := fmt.Sprintf("embed-sync-%d", maxSyncJobHistory+2)
	if _, ok := manager.Get(newest); !ok {
		t.Fatalf("newest job %s missing from history", newest)
	}
}

func TestSyncJobManagerFailedJobCarriesError(t *testing.T) {
	ctx := context.Background()
	service := newJobService(t, &jobEmbedder{failOnCall: 1}, 1)
	manager := NewSyncJobManager(service)

	job, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	job = waitForJobTerminal(t, manager, job.ID)
	if job.Status != SyncJobStatusFailed {
		t.Fatalf("job status = %q, want failed", job.Status)
	}
	if !strings.Contains(job.Err, "job embedder failure on call 1") {
		t.Fatalf("job Err = %q, want embedder failure", job.Err)
	}
	if job.Result != nil {
		t.Fatalf("failed job Result = %#v, want nil", job.Result)
	}
	if job.FinishedAt == nil {
		t.Fatal("failed job FinishedAt = nil, want set")
	}

	retry, err := manager.Start(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("retry Start() error = %v", err)
	}
	if retry.ID != "embed-sync-2" {
		t.Fatalf("retry job ID = %q, want embed-sync-2", retry.ID)
	}
	waitForJobTerminal(t, manager, retry.ID)
}
