package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

type fakeCommitter struct {
	ensureCalls int
	commitCalls int
	lastMessage string
}

func (f *fakeCommitter) EnsureRepo(ctx context.Context, repoDir string) error {
	_ = ctx
	_ = repoDir
	f.ensureCalls++
	return nil
}

func (f *fakeCommitter) CommitAll(ctx context.Context, repoDir, message string) (string, error) {
	_ = ctx
	_ = repoDir
	f.commitCalls++
	f.lastMessage = message
	return "sha-test", nil
}

func TestStoreInitCreatesScaffold(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, committer)

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	requiredPaths := []string{
		filepath.Join(root, "history", "turns"),
		filepath.Join(root, "notes", "inbox"),
		filepath.Join(root, "notes", "zettel"),
		filepath.Join(root, "notes", "maps"),
		filepath.Join(root, "state", "head.json"),
		filepath.Join(root, "README.md"),
	}
	for _, path := range requiredPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected path %q to exist: %v", path, err)
		}
	}

	if committer.ensureCalls != 1 {
		t.Fatalf("EnsureRepo calls = %d, want 1", committer.ensureCalls)
	}
	if committer.commitCalls != 1 {
		t.Fatalf("CommitAll calls = %d, want 1", committer.commitCalls)
	}
}

func TestStoreAppendAndLoadRecentMessages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []agent.Message{
		{Role: agent.UserRole, Content: "one"},
		{Role: agent.AssistantRole, Content: "first"},
	}); err != nil {
		t.Fatalf("AppendTurn(one) error = %v", err)
	}
	if err := store.AppendTurn(context.Background(), []agent.Message{
		{Role: agent.UserRole, Content: "two"},
		{Role: agent.AssistantRole, Content: "second"},
	}); err != nil {
		t.Fatalf("AppendTurn(two) error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(got))
	}
	if got[0].Content != "two" || got[1].Content != "second" {
		t.Fatalf("LoadRecentMessages contents = %#v, want latest turn only", got)
	}
}
