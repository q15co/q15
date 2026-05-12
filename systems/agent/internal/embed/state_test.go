package embed

import (
	"context"
	"testing"
)

func TestStateStorePersistsPointAndSyncState(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}

	record := stateRecord{
		PointID:     "point-1",
		SourceID:    "source-1",
		Collection:  CollectionCore,
		Path:        "/memory/core/a.md",
		Identity:    "/memory/core/a.md",
		ContentHash: "hash-a",
	}
	if err := state.upsertPoint(ctx, record); err != nil {
		t.Fatalf("upsertPoint() error = %v", err)
	}
	if err := state.storeSyncResult(ctx, Source{
		ID:         "source-1",
		Collection: CollectionCore,
	}, SyncResult{Scanned: 1, Embedded: 1, Upserted: 1}); err != nil {
		t.Fatalf("storeSyncResult() error = %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("reopen OpenState() error = %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.findByIdentity(ctx, CollectionCore, "source-1", "/memory/core/a.md")
	if err != nil {
		t.Fatalf("findByIdentity() error = %v", err)
	}
	if !ok || got.PointID != "point-1" {
		t.Fatalf("findByIdentity() = %#v, %v; want point-1", got, ok)
	}
	got, ok, err = reopened.findByContentHash(ctx, CollectionCore, "source-1", "hash-a")
	if err != nil {
		t.Fatalf("findByContentHash() error = %v", err)
	}
	if !ok || got.Identity != "/memory/core/a.md" {
		t.Fatalf("findByContentHash() = %#v, %v; want original identity", got, ok)
	}

	statuses, err := reopened.sourceStatuses(ctx)
	if err != nil {
		t.Fatalf("sourceStatuses() error = %v", err)
	}
	if len(statuses) != 1 || statuses[0].Points != 1 || statuses[0].LastSynced.IsZero() {
		t.Fatalf("sourceStatuses() = %#v, want one synced point", statuses)
	}
}

func TestStateStoreUpdatesDeletesAndRemovesSource(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()

	record := stateRecord{
		PointID:     "point-1",
		SourceID:    "source-1",
		Collection:  CollectionSemantic,
		Path:        "/memory/semantic/a.md",
		Identity:    "/memory/semantic/a.md",
		ContentHash: "hash-a",
	}
	if err := state.upsertPoint(ctx, record); err != nil {
		t.Fatalf("upsertPoint() error = %v", err)
	}
	record.Path = "/memory/semantic/b.md"
	record.Identity = "/memory/semantic/b.md"
	if err := state.upsertPoint(ctx, record); err != nil {
		t.Fatalf("move upsertPoint() error = %v", err)
	}

	if _, ok, err := state.findByIdentity(ctx, CollectionSemantic, "source-1", "/memory/semantic/a.md"); err != nil {
		t.Fatalf("old findByIdentity() error = %v", err)
	} else if ok {
		t.Fatal("old identity still exists after moved upsert")
	}
	got, ok, err := state.findByContentHash(ctx, CollectionSemantic, "source-1", "hash-a")
	if err != nil {
		t.Fatalf("findByContentHash() error = %v", err)
	}
	if !ok || got.Identity != "/memory/semantic/b.md" {
		t.Fatalf("findByContentHash() = %#v, %v; want moved identity", got, ok)
	}

	if err := state.storeSyncResult(ctx, Source{
		ID:         "source-1",
		Collection: CollectionSemantic,
	}, SyncResult{Scanned: 1, Unchanged: 1}); err != nil {
		t.Fatalf("storeSyncResult() error = %v", err)
	}
	if err := state.deletePointIDs(ctx, []string{"point-1"}); err != nil {
		t.Fatalf("deletePointIDs() error = %v", err)
	}
	records, err := state.recordsForSource(ctx, "source-1")
	if err != nil {
		t.Fatalf("recordsForSource() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("recordsForSource() = %#v, want none", records)
	}
	statuses, err := state.sourceStatuses(ctx)
	if err != nil {
		t.Fatalf("sourceStatuses() error = %v", err)
	}
	if len(statuses) != 1 || statuses[0].Points != 0 {
		t.Fatalf("sourceStatuses() after delete = %#v, want zero-point sync state", statuses)
	}

	if err := state.deleteSource(ctx, "source-1"); err != nil {
		t.Fatalf("deleteSource() error = %v", err)
	}
	statuses, err = state.sourceStatuses(ctx)
	if err != nil {
		t.Fatalf("sourceStatuses() after delete source error = %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("sourceStatuses() after delete source = %#v, want none", statuses)
	}
}
