package subagent

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionNameCatalogDoesNotRepeatPairs(t *testing.T) {
	if len(sessionNamePools) < 16 || sessionNameCount() < 50000 {
		t.Fatal("name catalog does not cover the intended range")
	}
	for _, pool := range sessionNamePools {
		if pool.locale == "" || len(pool.given) == 0 || len(pool.family) == 0 {
			t.Fatalf("empty name pool: %+v", pool)
		}
	}
	pattern := regexp.MustCompile(`^[a-z]{2,10}-[a-z]{2,10}-[1-9][0-9]*$`)
	seen := make(map[string]bool)
	for sequence := uint64(1); sequence <= sessionNameCount(); sequence++ {
		id := sessionID(sequence)
		if !pattern.MatchString(id) ||
			!strings.HasSuffix(id, "-"+strconv.FormatUint(sequence, 10)) {
			t.Fatalf("invalid session id: %q", id)
		}
		name := id[:strings.LastIndex(id, "-")]
		if seen[name] {
			t.Fatalf("name %q repeated before the catalog was exhausted", name)
		}
		seen[name] = true
	}
	first, wrapped := sessionID(1), sessionID(sessionNameCount()+1)
	if first == wrapped ||
		strings.TrimSuffix(first, "-1") != wrapped[:strings.LastIndex(wrapped, "-")] {
		t.Fatalf("catalog wrap lost its unique suffix: %q, %q", first, wrapped)
	}
	if id := sessionID(math.MaxUint64); !pattern.MatchString(id) {
		t.Fatalf("invalid session id at sequence limit: %q", id)
	}
}

func TestSessionSequenceSurvivesRestartAndRecordRemoval(t *testing.T) {
	root := t.TempDir()
	manager := persistentTestManager(t, root, &recordingModel{})
	first, err := manager.Start(StartRequest{ModelRef: "child", Task: "first"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, first)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first.recordPath); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, &recordingModel{})
	next, err := restarted.Start(StartRequest{ModelRef: "child", Task: "after cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, next)
	if !strings.HasSuffix(first.ID, "-1") || !strings.HasSuffix(next.ID, "-2") {
		t.Fatalf("session sequence reset after record removal: %q, %q", first.ID, next.ID)
	}
	info, err := os.Stat(filepath.Join(root, ".sequence"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("sequence file metadata = %v, %v", info, err)
	}
}

func TestSessionSequencePreservesLegacyRecords(t *testing.T) {
	root := t.TempDir()
	store, err := openRecordStore(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Session{
		ID:    "subagent-ee146328-c82d-407e-a804-c308d5c6efc6-1",
		Model: "child", Task: "legacy task", Status: "running",
		CreatedAt: time.Now(), runID: "legacy-run",
	}
	if err := store.save(legacy.recordLocked()); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	manager := persistentTestManager(t, root, &recordingModel{})
	restored, ok := manager.Get(legacy.ID)
	if !ok || restored.ID != legacy.ID || restored.status() != "interrupted" {
		t.Fatalf("legacy record was not preserved: %v", restored)
	}
	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "new name"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	if strings.HasPrefix(session.ID, "subagent-") || !strings.HasSuffix(session.ID, "-1") {
		t.Fatalf("new session did not use a friendly id: %q", session.ID)
	}
}

func TestRestoreRejectsMissingOrInvalidSessionSequence(t *testing.T) {
	for _, content := range []string{"missing", "", "0\n", "-1\n", "not-a-number", "18446744073709551616", strings.Repeat("1", 64)} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			manager := persistentTestManager(t, root, &recordingModel{})
			session, err := manager.Start(StartRequest{ModelRef: "child", Task: "keep identity"})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, session)
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".sequence")
			if content == "missing" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte(content), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			restarted := newTestManager(t, &recordingModel{})
			if err := restarted.Restore(root); err == nil ||
				!strings.Contains(err.Error(), "sequence") {
				t.Fatalf("Restore() accepted an unsafe sequence: %v", err)
			}
			if _, err := readSessionRecord(session.recordPath); err != nil {
				t.Fatalf("failed restoration changed the session record: %v", err)
			}
		})
	}
}

func TestSessionSequenceReservationFailurePreventsLaunch(t *testing.T) {
	root := t.TempDir()
	model := &recordingModel{}
	manager := persistentTestManager(t, root, model)
	path := filepath.Join(root, ".sequence")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Start(StartRequest{ModelRef: "child", Task: "must not launch"})
	if err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("Start() succeeded without reserving its id: %v", err)
	}
	if len(model.snapshotCalls()) != 0 || len(manager.List("all", 0)) != 0 {
		t.Fatal("a session launched without a durable sequence")
	}
}

func TestSessionRecordFailureDoesNotReuseReservedSequence(t *testing.T) {
	root := t.TempDir()
	model := &recordingModel{}
	manager := persistentTestManager(t, root, model)
	blockedPath := manager.records.path(sessionID(1))
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(StartRequest{ModelRef: "child", Task: "cannot record"}); err == nil {
		t.Fatal("Start() succeeded without its initial record")
	}
	if len(model.snapshotCalls()) != 0 {
		t.Fatal("a session launched after its record failed")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blockedPath); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, model)
	session, err := restarted.Start(StartRequest{ModelRef: "child", Task: "new reservation"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	if !strings.HasSuffix(session.ID, "-2") {
		t.Fatalf("failed launch reused its reserved sequence: %q", session.ID)
	}
}

func TestSessionSequenceConcurrentAllocations(t *testing.T) {
	root := t.TempDir()
	manager := persistentTestManager(t, root, &recordingModel{})
	const count = 16
	ids := make(chan string, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Go(func() {
			session, err := manager.Start(StartRequest{ModelRef: "child", Task: "concurrent"})
			if err != nil {
				t.Error(err)
				return
			}
			<-session.done
			ids <- session.ID
		})
	}
	workers.Wait()
	close(ids)
	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("concurrent allocation reused %q", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d sessions, want %d", len(seen), count)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, &recordingModel{})
	session, err := restarted.Start(StartRequest{ModelRef: "child", Task: "after concurrency"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, session)
	if !strings.HasSuffix(session.ID, fmt.Sprintf("-%d", count+1)) {
		t.Fatalf("concurrent sequence was not durable: %q", session.ID)
	}
}

func TestSessionSequenceExhaustionPreventsReuse(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".sequence"),
		[]byte(strconv.FormatUint(math.MaxUint64, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{}
	manager := persistentTestManager(t, root, model)
	if _, err := manager.Start(StartRequest{ModelRef: "child", Task: "must not wrap"}); err == nil {
		t.Fatal("Start() wrapped an exhausted sequence")
	}
	if len(model.snapshotCalls()) != 0 {
		t.Fatal("an exhausted sequence launched a session")
	}
}
