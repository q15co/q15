package cognition

import (
	"context"
	"strings"
	"testing"
)

type changingBuildLoader struct {
	*semanticMemoryJobLoader
	changePath string
	onRead     bool
	changed    bool
	began      bool
}

func (l *changingBuildLoader) ReadFileBytes(
	ctx context.Context,
	relative string,
) ([]byte, bool, error) {
	raw, exists, err := l.semanticMemoryJobLoader.ReadFileBytes(ctx, relative)
	if l.onRead && relative == l.changePath && !l.changed {
		l.rawFiles[relative] = string(raw) + "\n- Concurrent update.\n"
		l.changed = true
	}
	return raw, exists, err
}

func (l *changingBuildLoader) BeginMemoryRecovery(
	context.Context,
	map[string]FileSnapshot,
	string,
) (string, error) {
	l.began = true
	return "unexpected-journal", nil
}

func TestSemanticBuildRejectsChangedPromptOrBaselineBeforePreparingRecovery(t *testing.T) {
	for _, test := range []struct {
		name, path string
		onRead     bool
	}{
		{name: "working prompt", path: workingMemoryRelativePath},
		{name: "semantic prompt", path: semanticFactsRelativePath},
		{name: "ledger read", path: "working/LEDGER.md"},
		{name: "working baseline", path: workingMemoryRelativePath, onRead: true},
		{name: "open-loops baseline", path: "working/OPEN_LOOPS.md", onRead: true},
		{name: "topic baseline", path: "semantic/topics/existing.txt", onRead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			loader := &changingBuildLoader{
				semanticMemoryJobLoader: goldenSemanticLoader(),
				changePath:              test.path,
				onRead:                  test.onRead,
			}
			loader.rawFiles = map[string]string{"semantic/topics/existing.txt": "Existing topic.\n"}
			if !test.onRead {
				raw, _, err := loader.semanticMemoryJobLoader.ReadFileBytes(
					context.Background(),
					test.path,
				)
				if err != nil {
					t.Fatal(err)
				}
				loader.rawFiles[test.path] = string(raw) + "\n- Concurrent update.\n"
			}
			_, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
			if err == nil ||
				!strings.Contains(err.Error(), "memory changed while building job evidence") {
				t.Fatalf("Build = %v, want incoherent-evidence rejection", err)
			}
			if loader.began {
				t.Fatal("Build prepared recovery after detecting incoherent evidence")
			}
		})
	}
}

func TestMemoryBuildEvidenceRejectsInventoryChanges(t *testing.T) {
	for _, added := range []bool{false, true} {
		baseline := map[string]string{"/memory/semantic/topics/existing.txt": "before"}
		snapshots := map[string]FileSnapshot{
			"/memory/semantic/topics/existing.txt": {Existed: true, Content: []byte("before")},
		}
		if added {
			snapshots["/memory/semantic/topics/new.txt"] = FileSnapshot{
				Existed: true,
				Content: []byte("new"),
			}
		} else {
			delete(snapshots, "/memory/semantic/topics/existing.txt")
		}
		if err := validateMemoryBuildEvidence(snapshots, baseline, nil); err == nil {
			t.Fatalf("accepted inventory change (added=%v)", added)
		}
	}
}
