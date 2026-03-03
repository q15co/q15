package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	store := NewStore(root, "Jared", committer)

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	requiredPaths := []string{
		filepath.Join(root, "core", "AGENT.md"),
		filepath.Join(root, "core", "USER.md"),
		filepath.Join(root, "core", "SOUL.md"),
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

func TestStoreLoadCoreMemory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	core, err := store.LoadCoreMemory(context.Background())
	if err != nil {
		t.Fatalf("LoadCoreMemory() error = %v", err)
	}
	if len(core.Files) < 3 {
		t.Fatalf("core files len = %d, want at least 3 seeded files", len(core.Files))
	}
	foundAgent := false
	for _, file := range core.Files {
		if file.RelativePath == "core/AGENT.md" {
			foundAgent = true
			if file.Description == "" || file.Limit == 0 || file.Content == "" {
				t.Fatalf("AGENT.md core file missing parsed frontmatter/body: %#v", file)
			}
			if !strings.Contains(file.Content, "You are Jared, a pragmatic software assistant.") {
				t.Fatalf("AGENT.md did not render configured agent name: %q", file.Content)
			}
			if !strings.Contains(file.Content, "Operate as an autonomous agent") {
				t.Fatalf("AGENT.md missing autonomous guidance: %q", file.Content)
			}
			if !strings.Contains(file.Content, "do not narrate routine, low-risk tool calls") {
				t.Fatalf("AGENT.md missing no-narration tool-call guidance: %q", file.Content)
			}
			if !strings.Contains(
				file.Content,
				"Do not ask extra authorization for routine user-requested reads/writes",
			) {
				t.Fatalf("AGENT.md missing routine-authorization guidance: %q", file.Content)
			}
			break
		}
	}
	if !foundAgent {
		t.Fatalf("expected core/AGENT.md in loaded core files: %#v", core.Files)
	}
}

func TestStoreAppendAndLoadRecentMessages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

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

func TestStoreInterruptedTurnPersistsAndReplays(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	interrupted := []agent.Message{
		{Role: agent.UserRole, Content: "do a long task"},
		{
			Role: agent.AssistantRole,
			ToolCalls: []agent.ToolCall{
				{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`},
			},
		},
		{Role: agent.ToolRole, ToolCallID: "call-1", Content: "tool-output"},
		{
			Role:    agent.AssistantRole,
			Content: "Run stopped by internal safety guard: reached maximum tool-call turns (96). Progress up to this point has been saved.",
		},
	}
	if err := store.AppendTurn(context.Background(), interrupted); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != len(interrupted) {
		t.Fatalf("LoadRecentMessages len = %d, want %d", len(got), len(interrupted))
	}
	if got[len(got)-1].Role != agent.AssistantRole {
		t.Fatalf("last replayed role = %q, want assistant", got[len(got)-1].Role)
	}
	if !strings.Contains(got[len(got)-1].Content, "internal safety guard") {
		t.Fatalf("last replayed message = %q", got[len(got)-1].Content)
	}
}

func TestParseMarkdownFrontmatter(t *testing.T) {
	t.Run("metadata and body", func(t *testing.T) {
		raw := `---
description: "Core behavior: keep output concise"
limit: 4096
---

# Title

Body paragraph.
`
		description, limit, body := parseMarkdownFrontmatter(raw)
		if description != "Core behavior: keep output concise" {
			t.Fatalf("description = %q, want %q", description, "Core behavior: keep output concise")
		}
		if limit != 4096 {
			t.Fatalf("limit = %d, want %d", limit, 4096)
		}
		if body != "# Title\n\nBody paragraph." {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("no frontmatter", func(t *testing.T) {
		raw := "# Header\n\nText"
		description, limit, body := parseMarkdownFrontmatter(raw)
		if description != "" {
			t.Fatalf("description = %q, want empty", description)
		}
		if limit != 0 {
			t.Fatalf("limit = %d, want 0", limit)
		}
		if body != "# Header\n\nText" {
			t.Fatalf("body = %q, want original markdown", body)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		raw := `---
description: [unterminated
---
hello`
		description, limit, body := parseMarkdownFrontmatter(raw)
		if description != "" {
			t.Fatalf("description = %q, want empty", description)
		}
		if limit != 0 {
			t.Fatalf("limit = %d, want 0", limit)
		}
		if body != "hello" {
			t.Fatalf("body = %q, want %q", body, "hello")
		}
	})
}
