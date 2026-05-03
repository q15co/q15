package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
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
		filepath.Join(root, "semantic"),
		filepath.Join(root, "semantic", "facts.md"),
		filepath.Join(root, "semantic", "preferences.md"),
		filepath.Join(root, "semantic", "projects.md"),
		filepath.Join(root, "working"),
		filepath.Join(root, "working", "WORKING_MEMORY.md"),
		filepath.Join(root, "history", "turns"),
		filepath.Join(root, "history", "state", "head.json"),
		filepath.Join(root, "history", "state", "consolidation_checkpoint.json"),
		filepath.Join(root, "cognition", "state"),
		filepath.Join(root, "cognition", "state", "semantic_extraction_checkpoint.json"),
		filepath.Join(root, "cognition", "indexer"),
		filepath.Join(root, "cognition", "triggers", "jobs"),
		filepath.Join(root, "cognition", "runs"),
		filepath.Join(root, "notes", "inbox"),
		filepath.Join(root, "notes", "zettel"),
		filepath.Join(root, "notes", "maps"),
		filepath.Join(root, "README.md"),
	}
	for _, path := range requiredPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected path %q to exist: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("legacy state dir should not be created, err = %v", err)
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	for _, want := range []string{
		"Core self-model files",
		"semantic/facts.md, semantic/preferences.md, and semantic/projects.md",
		"Semantic memory is tool-fetched for cognition jobs and is not auto-injected",
		"working/WORKING_MEMORY.md",
		"notes/ is never working memory",
		"history/state/head.json",
		"history/state/consolidation_checkpoint.json",
		"cognition/",
		"cognition/state/",
		"cognition/state/semantic_extraction_checkpoint.json",
		"cognition/triggers/jobs/",
		"cognition/runs/",
		"zettelkasten layout",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing %q:\n%s", want, string(readme))
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
			if !strings.Contains(
				file.Content,
				"Default to a direct, practical collaboration style.",
			) {
				t.Fatalf("AGENT.md missing durable collaboration preference: %q", file.Content)
			}
			if !strings.Contains(
				file.Content,
				"Treat runtime policy about tool use, completion, and safety as code-owned",
			) {
				t.Fatalf("AGENT.md missing code-owned policy reminder: %q", file.Content)
			}
			if strings.Contains(file.Content, "Execute -> Verify -> Report") {
				t.Fatalf(
					"AGENT.md should no longer carry runtime execution policy: %q",
					file.Content,
				)
			}
			break
		}
	}
	if !foundAgent {
		t.Fatalf("expected core/AGENT.md in loaded core files: %#v", core.Files)
	}
}

func TestStoreLoadWorkingMemory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	notesPath := filepath.Join(root, "notes", "inbox", "todo.md")
	if err := os.WriteFile(notesPath, []byte("# Inbox\n- unrelated\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(notes) error = %v", err)
	}

	working, err := store.LoadWorkingMemory(context.Background())
	if err != nil {
		t.Fatalf("LoadWorkingMemory() error = %v", err)
	}
	if working.RelativePath != "working/WORKING_MEMORY.md" {
		t.Fatalf("RelativePath = %q, want %q", working.RelativePath, "working/WORKING_MEMORY.md")
	}
	for _, want := range []string{
		"# Working Memory",
		"## Current Priorities",
		"## Active Tasks",
		"## Open Threads",
		"## Recent Progress",
		"## Pending Checks",
		"## Temporary Context",
	} {
		if !strings.Contains(working.Content, want) {
			t.Fatalf("working memory missing %q:\n%s", want, working.Content)
		}
	}
	if strings.Contains(working.Content, "# Inbox") {
		t.Fatalf("working memory should not include notes content:\n%s", working.Content)
	}
}

func TestStoreLoadSemanticMemory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	unrelatedPath := filepath.Join(root, "semantic", "scratch.md")
	if err := os.WriteFile(unrelatedPath, []byte("# Scratch\n- ignore me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(unrelated semantic file) error = %v", err)
	}

	semantic, err := store.LoadSemanticMemory(context.Background())
	if err != nil {
		t.Fatalf("LoadSemanticMemory() error = %v", err)
	}

	gotPaths := make([]string, 0, len(semantic.Files))
	for _, file := range semantic.Files {
		gotPaths = append(gotPaths, file.RelativePath)
	}
	wantPaths := []string{
		"semantic/facts.md",
		"semantic/preferences.md",
		"semantic/projects.md",
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("semantic paths = %v, want %v", gotPaths, wantPaths)
	}

	for _, file := range semantic.Files {
		switch file.RelativePath {
		case "semantic/facts.md":
			for _, want := range []string{"# Semantic Facts", "## Confirmed Facts", "## Grounded Inferences"} {
				if !strings.Contains(file.Content, want) {
					t.Fatalf("facts content missing %q:\n%s", want, file.Content)
				}
			}
		case "semantic/preferences.md":
			for _, want := range []string{
				"# Semantic Preferences",
				"## User Preferences",
				"## Collaboration Preferences",
			} {
				if !strings.Contains(file.Content, want) {
					t.Fatalf("preferences content missing %q:\n%s", want, file.Content)
				}
			}
		case "semantic/projects.md":
			for _, want := range []string{
				"# Semantic Projects",
				"## Active Projects",
				"## Durable Project Knowledge",
			} {
				if !strings.Contains(file.Content, want) {
					t.Fatalf("projects content missing %q:\n%s", want, file.Content)
				}
			}
		default:
			t.Fatalf("unexpected semantic file loaded: %#v", file)
		}
	}
}

func TestStoreLoadSemanticMemoryFallsBackToEmbeddedSeedWhenFileIsMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	path := filepath.Join(root, "semantic", "facts.md")
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(facts.md) error = %v", err)
	}

	semantic, err := store.LoadSemanticMemory(context.Background())
	if err != nil {
		t.Fatalf("LoadSemanticMemory() error = %v", err)
	}

	if got, want := len(semantic.Files), 3; got != want {
		t.Fatalf("semantic files len = %d, want %d", got, want)
	}

	for _, file := range semantic.Files {
		if file.RelativePath != "semantic/facts.md" {
			continue
		}
		for _, want := range []string{
			"# Semantic Facts",
			"## Confirmed Facts",
			"## Grounded Inferences",
		} {
			if !strings.Contains(file.Content, want) {
				t.Fatalf("facts fallback content missing %q:\n%s", want, file.Content)
			}
		}
		return
	}

	t.Fatalf("semantic files missing facts.md: %#v", semantic.Files)
}

func TestStoreAppendAndLoadRecentMessages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("one"),
		conversation.AssistantMessage(conversation.Text("first", "")),
	}); err != nil {
		t.Fatalf("AppendTurn(one) error = %v", err)
	}
	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("two"),
		conversation.AssistantMessage(conversation.Text("second", "")),
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
	if conversation.TextValue(got[0]) != "two" || conversation.TextValue(got[1]) != "second" {
		t.Fatalf("LoadRecentMessages contents = %#v, want latest turn only", got)
	}
}

func TestStoreLoadLatestMessagesIgnoresConsolidationCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, pair := range [][2]string{
		{"one", "first"},
		{"two", "second"},
		{"three", "third"},
	} {
		if err := store.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(pair[0]),
			conversation.AssistantMessage(conversation.Text(pair[1], "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", pair[0], err)
		}
	}

	if _, err := store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 2},
	); err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}

	checkpointAware, err := store.LoadRecentMessages(context.Background(), 2)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(checkpointAware) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(checkpointAware))
	}
	if got, want := conversation.TextValue(checkpointAware[0]), "three"; got != want {
		t.Fatalf("LoadRecentMessages first message = %q, want %q", got, want)
	}

	tail, err := store.LoadLatestMessages(context.Background(), 2)
	if err != nil {
		t.Fatalf("LoadLatestMessages() error = %v", err)
	}
	if len(tail) != 4 {
		t.Fatalf("LoadLatestMessages len = %d, want 4", len(tail))
	}
	if got, want := conversation.TextValue(tail[0]), "two"; got != want {
		t.Fatalf("LoadLatestMessages first message = %q, want %q", got, want)
	}
	if got, want := conversation.TextValue(tail[2]), "three"; got != want {
		t.Fatalf("LoadLatestMessages third message = %q, want %q", got, want)
	}
}

func TestStoreLoadMessagesSinceSeq(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, pair := range [][2]string{
		{"one", "first"},
		{"two", "second"},
		{"three", "third"},
	} {
		if err := store.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(pair[0]),
			conversation.AssistantMessage(conversation.Text(pair[1], "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", pair[0], err)
		}
	}

	got, err := store.LoadMessagesSinceSeq(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadMessagesSinceSeq() error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("LoadMessagesSinceSeq len = %d, want 4", len(got))
	}
	if conversation.TextValue(got[0]) != "two" ||
		conversation.TextValue(got[1]) != "second" ||
		conversation.TextValue(got[2]) != "three" ||
		conversation.TextValue(got[3]) != "third" {
		t.Fatalf("LoadMessagesSinceSeq contents = %#v, want turns after seq 1", got)
	}

	got, err = store.LoadMessagesSinceSeq(context.Background(), 3)
	if err != nil {
		t.Fatalf("LoadMessagesSinceSeq(at head) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadMessagesSinceSeq(at head) len = %d, want 0", len(got))
	}
}

func TestStoreAppendAndLoadRecentMessagesPreservesUserTemporalMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	timestamp := time.Date(2026, time.April, 12, 10, 11, 12, 0, time.FixedZone("UTC+2", 2*60*60))
	if err := store.AppendTurn(context.Background(), []conversation.Message{
		{
			Role:  conversation.UserRole,
			Parts: []conversation.Part{conversation.Text("one", "")},
			UserTemporal: &conversation.UserTemporalMetadata{
				TimeLocal:            timestamp,
				SincePrevUserMessage: conversation.NewDuration(3*time.Minute + 42*time.Second),
			},
		},
		conversation.AssistantMessage(conversation.Text("first", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(got))
	}
	if stored, ok := conversation.UserMessageTimeLocal(got[0]); !ok || !stored.Equal(timestamp) {
		t.Fatalf("stored timestamp = %v, %t, want %v", stored, ok, timestamp)
	}
	if gap, ok := conversation.SincePrevUserMessage(got[0]); !ok ||
		gap != 3*time.Minute+42*time.Second {
		t.Fatalf("stored gap = %s, %t, want 3m42s", gap, ok)
	}
}

func TestStoreLoadLastUserTimestampIgnoresTurnsWithoutMessageMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("legacy"),
		conversation.AssistantMessage(conversation.Text("first", "")),
	}); err != nil {
		t.Fatalf("AppendTurn(legacy) error = %v", err)
	}

	timestamp := time.Date(2026, time.April, 12, 10, 11, 12, 0, time.FixedZone("UTC+2", 2*60*60))
	if err := store.AppendTurn(context.Background(), []conversation.Message{
		{
			Role:  conversation.UserRole,
			Parts: []conversation.Part{conversation.Text("current", "")},
			UserTemporal: &conversation.UserTemporalMetadata{
				TimeLocal: timestamp,
			},
		},
		conversation.AssistantMessage(conversation.Text("second", "")),
	}); err != nil {
		t.Fatalf("AppendTurn(current) error = %v", err)
	}

	got, ok, err := store.LoadLastUserTimestamp(context.Background())
	if err != nil {
		t.Fatalf("LoadLastUserTimestamp() error = %v", err)
	}
	if !ok {
		t.Fatal("LoadLastUserTimestamp() ok = false, want true")
	}
	if !got.Equal(timestamp) {
		t.Fatalf("LoadLastUserTimestamp() = %s, want %s", got, timestamp)
	}
}

func TestStoreLoadRecentMessagesAfterConsolidationCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, turn := range []struct {
		user      string
		assistant string
	}{
		{user: "one", assistant: "first"},
		{user: "two", assistant: "second"},
		{user: "three", assistant: "third"},
	} {
		if err := store.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(turn.user),
			conversation.AssistantMessage(conversation.Text(turn.assistant, "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", turn.user, err)
		}
	}

	checkpoint, err := store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 2},
	)
	if err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}
	if checkpoint.LastConsolidatedTurnID != "turn-00000000000000000002" {
		t.Fatalf(
			"checkpoint.LastConsolidatedTurnID = %q, want %q",
			checkpoint.LastConsolidatedTurnID,
			"turn-00000000000000000002",
		)
	}

	got, err := store.LoadRecentMessages(context.Background(), 6)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(got))
	}
	if conversation.TextValue(got[0]) != "three" || conversation.TextValue(got[1]) != "third" {
		t.Fatalf("LoadRecentMessages contents = %#v, want checkpoint-relative replay", got)
	}
}

func TestStoreLoadRecentMessagesAfterConsolidationCheckpointRespectsTurnCap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, turn := range []struct {
		user      string
		assistant string
	}{
		{user: "one", assistant: "first"},
		{user: "two", assistant: "second"},
		{user: "three", assistant: "third"},
	} {
		if err := store.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(turn.user),
			conversation.AssistantMessage(conversation.Text(turn.assistant, "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", turn.user, err)
		}
	}

	if _, err := store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 1},
	); err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(got))
	}
	if conversation.TextValue(got[0]) != "three" || conversation.TextValue(got[1]) != "third" {
		t.Fatalf("LoadRecentMessages contents = %#v, want newest unconsolidated turn", got)
	}
}

func TestStoreLoadRecentMessagesAfterConsolidationCheckpointReturnsNoneAtHead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, turn := range []struct {
		user      string
		assistant string
	}{
		{user: "one", assistant: "first"},
		{user: "two", assistant: "second"},
	} {
		if err := store.AppendTurn(context.Background(), []conversation.Message{
			conversation.UserMessage(turn.user),
			conversation.AssistantMessage(conversation.Text(turn.assistant, "")),
		}); err != nil {
			t.Fatalf("AppendTurn(%q) error = %v", turn.user, err)
		}
	}

	if _, err := store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 2},
	); err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 6)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadRecentMessages len = %d, want 0", len(got))
	}
}

func TestStoreLoadRecentMessagesSupportsNonCanonicalTurnFilenames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	path := filepath.Join(root, "history", "turns", "2026", "04", "08", "legacy-turn.json")
	record := turnRecord{
		SchemaVersion: conversation.SchemaVersion,
		ID:            "turn-00000000000000000005",
		Seq:           5,
		CreatedAt:     time.Date(2026, time.April, 8, 12, 0, 0, 0, time.UTC),
		Messages: []conversation.Message{
			conversation.UserMessage("legacy"),
			conversation.AssistantMessage(conversation.Text("replayed", "")),
		},
	}
	if err := writeJSONFileAtomic(path, record); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadRecentMessages len = %d, want 2", len(got))
	}
	if conversation.TextValue(got[0]) != "legacy" || conversation.TextValue(got[1]) != "replayed" {
		t.Fatalf("LoadRecentMessages contents = %#v, want legacy turn replay", got)
	}
}

func TestStoreAppendAndLoadRecentMessagesWithImageParts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	want := []conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("inspect this", ""),
			conversation.Image("media://sha256/abc", ""),
		),
		conversation.AssistantMessage(conversation.Text("done", "")),
	}
	if err := store.AppendTurn(context.Background(), want); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("LoadRecentMessages len = %d, want %d", len(got), len(want))
	}
	if got[0].Parts[1].Type != conversation.ImagePartType ||
		got[0].Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("replayed image part = %#v", got[0].Parts[1])
	}
}

func TestStoreAppendTurnPromotesFinalReplyMediaToAssistant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("send the image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"cat.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded image: /workspace/cat.png", false),
				conversation.Image("media://sha256/cat", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("LoadRecentMessages len = %d, want 4", len(got))
	}
	if toolParts := got[2].Parts; len(toolParts) != 1 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("replayed tool parts = %#v, want tool_result only", toolParts)
	}
	if final := got[3]; len(final.Parts) != 2 ||
		final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/cat" {
		t.Fatalf("replayed final assistant = %#v, want text plus image", final)
	}
}

func TestStoreAppendTurnPromotesOnlyTrailingToolBatchToAssistant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("send the final image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"preview.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded preview", false),
				conversation.Image("media://sha256/preview", ""),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "load_image", `{"path":"final.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "Loaded final", false),
				conversation.Image("media://sha256/final", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("LoadRecentMessages len = %d, want 6", len(got))
	}
	if previewParts := got[2].Parts; len(previewParts) != 2 {
		t.Fatalf("preview tool parts = %#v, want preview tool result plus image", previewParts)
	}
	if finalToolParts := got[4].Parts; len(finalToolParts) != 1 ||
		finalToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("final tool parts = %#v, want tool_result only", finalToolParts)
	}
	if final := got[5]; len(final.Parts) != 2 ||
		final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/final" {
		t.Fatalf("replayed final assistant = %#v, want text plus final image", final)
	}
}

func TestStoreInterruptedTurnPersistsAndReplays(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	interrupted := []conversation.Message{
		conversation.UserMessage("do a long task"),
		conversation.AssistantMessage(
			conversation.Text("working", conversation.TextDispositionCommentary),
			conversation.ToolCall("call-1", "echo", `{"value":"x"}`),
		),
		conversation.ToolResultMessage("call-1", "tool-output", false),
		conversation.AssistantMessage(
			conversation.Text(
				"Run stopped by internal safety guard: reached maximum tool-call turns (96). Progress up to this point has been saved.",
				"",
			),
		),
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
	if got[len(got)-1].Role != conversation.AssistantRole {
		t.Fatalf("last replayed role = %q, want assistant", got[len(got)-1].Role)
	}
	if got[1].Parts[0].Disposition != conversation.TextDispositionCommentary {
		t.Fatalf(
			"replayed assistant disposition = %q, want %q",
			got[1].Parts[0].Disposition,
			conversation.TextDispositionCommentary,
		)
	}
	if calls := conversation.ToolCalls([]conversation.Message{got[1]}); len(calls) != 1 ||
		calls[0].ID != "call-1" {
		t.Fatalf("replayed assistant tool calls = %#v", calls)
	}
	if !strings.Contains(conversation.TextValue(got[len(got)-1]), "internal safety guard") {
		t.Fatalf("last replayed message = %q", conversation.TextValue(got[len(got)-1]))
	}
}

func TestStoreInitMigratesLegacyTurnsToCurrentSchemaAndSynchronizesHead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	legacyPath := filepath.Join(
		root,
		"history",
		"turns",
		"2026",
		"03",
		"29",
		"00000000000000000007.json",
	)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	record := legacyTurnRecord{
		ID:        "turn-00000000000000000007",
		Seq:       7,
		CreatedAt: time.Date(2026, time.March, 29, 12, 0, 0, 0, time.UTC),
		Messages: []legacyMessage{
			{Role: "user", Content: "hello"},
			{
				Role:    "assistant",
				Content: "thinking",
				Phase:   "commentary",
				ToolCalls: []agent.ToolCall{
					{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`},
				},
			},
			{Role: "tool", ToolCallID: "call-1", Content: "tool-output"},
			{Role: "assistant", Content: "done"},
		},
	}
	if err := writeJSONFileAtomic(legacyPath, record); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.lastMessage != "memory: upgrade transcript history to v3" {
		t.Fatalf(
			"commit message = %q, want %q",
			committer.lastMessage,
			"memory: upgrade transcript history to v3",
		)
	}

	turn, err := store.readTurn(legacyPath)
	if err != nil {
		t.Fatalf("readTurn() error = %v", err)
	}
	if turn.SchemaVersion != conversation.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", turn.SchemaVersion, conversation.SchemaVersion)
	}
	if turn.Seq != 7 {
		t.Fatalf("seq = %d, want 7", turn.Seq)
	}
	if got := turn.Messages[1].Parts[0].Disposition; got != conversation.TextDispositionCommentary {
		t.Fatalf("migrated disposition = %q, want %q", got, conversation.TextDispositionCommentary)
	}
	if calls := conversation.ToolCalls([]conversation.Message{turn.Messages[1]}); len(calls) != 1 ||
		calls[0].Name != "echo" {
		t.Fatalf("migrated tool calls = %#v", calls)
	}
	if part := turn.Messages[2].Parts[0]; part.Type != conversation.ToolResultPartType ||
		part.IsError {
		t.Fatalf("migrated tool result = %#v", part)
	}

	head, err := store.readHeadState()
	if err != nil {
		t.Fatalf("readHeadState() error = %v", err)
	}
	if head.LastSeq != 7 {
		t.Fatalf("head.LastSeq = %d, want 7", head.LastSeq)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("next"),
		conversation.AssistantMessage(conversation.Text("ok", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	head, err = store.readHeadState()
	if err != nil {
		t.Fatalf("readHeadState() after append error = %v", err)
	}
	if head.LastSeq != 8 {
		t.Fatalf("head.LastSeq after append = %d, want 8", head.LastSeq)
	}

	paths, err := store.listTurnPaths()
	if err != nil {
		t.Fatalf("listTurnPaths() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("turn paths len = %d, want 2", len(paths))
	}
	if got := filepath.Base(paths[len(paths)-1]); got != "00000000000000000008.json" {
		t.Fatalf("latest turn file = %q, want %q", got, "00000000000000000008.json")
	}
}

func TestStoreInitMigratesLegacyTitleCaseMessagesAndReasoning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	legacyPath := filepath.Join(
		root,
		"history",
		"turns",
		"2026",
		"03",
		"15",
		"00000000000000000003.json",
	)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	raw := `{
  "id": "turn-00000000000000000003",
  "seq": 3,
  "created_at": "2026-03-15T22:20:26.752331219Z",
  "messages": [
    {
      "Role": "user",
      "Content": "can you test if all your tools work as expected?"
    },
    {
      "Role": "assistant",
      "Content": "",
      "ToolCalls": [
        {
          "ID": "write_file:17",
          "Name": "write_file",
          "Arguments": "{\"path\":\"/workspace/test-tools.md\",\"content\":\"hello\"}"
        }
      ],
      "ProviderRaw": {
        "role": "assistant",
        "content": "",
        "reasoning_content": "Let me test the tools.",
        "tool_calls": [
          {
            "id": "write_file:17",
            "type": "function",
            "function": {
              "name": "write_file",
              "arguments": "{\"path\":\"/workspace/test-tools.md\",\"content\":\"hello\"}"
            }
          }
        ]
      }
    },
    {
      "Role": "tool",
      "Content": "Path: /workspace/test-tools.md\nBytes-Written: 5",
      "ToolCallID": "write_file:17"
    }
  ]
}`
	if err := os.WriteFile(legacyPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	turn, err := store.readTurn(legacyPath)
	if err != nil {
		t.Fatalf("readTurn() error = %v", err)
	}
	if len(turn.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(turn.Messages))
	}
	if turn.Messages[1].Role != conversation.AssistantRole {
		t.Fatalf("assistant role = %q, want assistant", turn.Messages[1].Role)
	}
	if len(turn.Messages[1].Parts) != 2 {
		t.Fatalf("assistant parts len = %d, want 2", len(turn.Messages[1].Parts))
	}
	if turn.Messages[1].Parts[0].Type != conversation.ReasoningPartType ||
		turn.Messages[1].Parts[0].Text != "Let me test the tools." {
		t.Fatalf("assistant reasoning part = %#v", turn.Messages[1].Parts[0])
	}
	calls := conversation.ToolCalls([]conversation.Message{turn.Messages[1]})
	if len(calls) != 1 || calls[0].ID != "write_file:17" || calls[0].Name != "write_file" {
		t.Fatalf("assistant tool calls = %#v", calls)
	}
	if part := turn.Messages[2].Parts[0]; part.Type != conversation.ToolResultPartType ||
		part.ToolCallID != "write_file:17" {
		t.Fatalf("tool result part = %#v", part)
	}
}

func TestStoreInitQuarantinesUnreadableLegacyTurns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	legacyPath := filepath.Join(root, "history", "turns", "2026", "03", "29", "broken.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.lastMessage != "memory: upgrade transcript history to v3" {
		t.Fatalf(
			"commit message = %q, want %q",
			committer.lastMessage,
			"memory: upgrade transcript history to v3",
		)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy path should be removed, stat error = %v", err)
	}

	quarantinePath := filepath.Join(
		root,
		"history",
		"quarantine",
		"2026",
		"03",
		"29",
		"broken.json",
	)
	if _, err := os.Stat(quarantinePath); err != nil {
		t.Fatalf("expected quarantined file %q: %v", quarantinePath, err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadRecentMessages len = %d, want 0", len(got))
	}
}

func TestStoreInitSanitizesExistingV2TurnsIntoCurrentSchema(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	turnPath := filepath.Join(
		root,
		"history",
		"turns",
		"2026",
		"03",
		"29",
		"00000000000000000003.json",
	)
	if err := os.MkdirAll(filepath.Dir(turnPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	record := turnRecord{
		SchemaVersion: conversation.SchemaVersion,
		ID:            "turn-00000000000000000003",
		Seq:           3,
		CreatedAt:     time.Date(2026, time.March, 15, 22, 20, 26, 0, time.UTC),
		Messages: []conversation.Message{
			conversation.UserMessage("can you test if all your tools work as expected?"),
			{Role: conversation.AssistantRole},
			{Role: conversation.ToolRole},
			conversation.AssistantMessage(conversation.Text("All tools operational.", "")),
		},
	}
	if err := writeJSONFileAtomic(turnPath, record); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.lastMessage != "memory: upgrade transcript history to v3" {
		t.Fatalf(
			"commit message = %q, want %q",
			committer.lastMessage,
			"memory: upgrade transcript history to v3",
		)
	}

	turn, err := store.readTurn(turnPath)
	if err != nil {
		t.Fatalf("readTurn() error = %v", err)
	}
	if len(turn.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(turn.Messages))
	}
	if turn.Messages[0].Role != conversation.UserRole ||
		turn.Messages[1].Role != conversation.AssistantRole {
		t.Fatalf("sanitized roles = %#v", turn.Messages)
	}
}

func TestStoreInitPromotesStoredToolReplyImagesToFinalAssistant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	turnPath := filepath.Join(
		root,
		"history",
		"turns",
		"2026",
		"03",
		"29",
		"00000000000000000005.json",
	)
	if err := os.MkdirAll(filepath.Dir(turnPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	record := turnRecord{
		SchemaVersion: conversation.SchemaVersion,
		ID:            "turn-00000000000000000005",
		Seq:           5,
		CreatedAt:     time.Date(2026, time.March, 29, 12, 0, 0, 0, time.UTC),
		Messages: []conversation.Message{
			conversation.UserMessage("send the image"),
			conversation.AssistantMessage(
				conversation.ToolCall("call-1", "load_image", `{"path":"cat.png"}`),
			),
			{
				Role: conversation.ToolRole,
				Parts: []conversation.Part{
					conversation.ToolResult("call-1", "Loaded image: /workspace/cat.png", false),
					conversation.Image("media://sha256/cat", ""),
				},
			},
			conversation.AssistantMessage(conversation.Text("done", "")),
		},
	}
	if err := writeJSONFileAtomic(turnPath, record); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.lastMessage != "memory: upgrade transcript history to v3" {
		t.Fatalf(
			"commit message = %q, want %q",
			committer.lastMessage,
			"memory: upgrade transcript history to v3",
		)
	}

	turn, err := store.readTurn(turnPath)
	if err != nil {
		t.Fatalf("readTurn() error = %v", err)
	}
	if len(turn.Messages) != 4 {
		t.Fatalf("messages len = %d, want 4", len(turn.Messages))
	}
	if toolParts := turn.Messages[2].Parts; len(toolParts) != 1 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("tool parts = %#v, want tool_result only", toolParts)
	}
	if final := turn.Messages[3]; len(final.Parts) != 2 ||
		final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/cat" {
		t.Fatalf("final assistant = %#v, want text plus image", final)
	}
}

func TestStoreInitBackfillsReplayOnlyReasoningForExistingToolReplay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	turnPath := filepath.Join(
		root,
		"history",
		"turns",
		"2026",
		"03",
		"29",
		"00000000000000000004.json",
	)
	if err := os.MkdirAll(filepath.Dir(turnPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	record := turnRecord{
		SchemaVersion: conversation.SchemaVersion,
		ID:            "turn-00000000000000000004",
		Seq:           4,
		CreatedAt:     time.Date(2026, time.March, 29, 12, 0, 0, 0, time.UTC),
		Messages: []conversation.Message{
			conversation.UserMessage("inspect memory"),
			conversation.AssistantMessage(conversation.Reasoning("", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`,
				),
			})),
			conversation.AssistantMessage(
				conversation.ToolCall("call-1", "exec", `{"command":"pwd"}`),
			),
			conversation.ToolResultMessage("call-1", "ok", false),
			conversation.AssistantMessage(conversation.Text("done", "")),
		},
	}
	if err := writeJSONFileAtomic(turnPath, record); err != nil {
		t.Fatalf("writeJSONFileAtomic() error = %v", err)
	}

	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.lastMessage != "memory: upgrade transcript history to v3" {
		t.Fatalf(
			"commit message = %q, want %q",
			committer.lastMessage,
			"memory: upgrade transcript history to v3",
		)
	}

	turn, err := store.readTurn(turnPath)
	if err != nil {
		t.Fatalf("readTurn() error = %v", err)
	}
	if got := turn.Messages[1].Parts[0].Text; got != conversation.PortableReasoningUnavailableText {
		t.Fatalf(
			"backfilled reasoning text = %q, want %q",
			got,
			conversation.PortableReasoningUnavailableText,
		)
	}
}

func TestStoreAppendTurnBackfillsReplayOnlyReasoningForToolReplay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("inspect memory"),
		conversation.AssistantMessage(conversation.Reasoning("", map[string]json.RawMessage{
			"openai_responses": json.RawMessage(`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`),
		})),
		conversation.AssistantMessage(conversation.ToolCall("call-1", "exec", `{"command":"pwd"}`)),
		conversation.ToolResultMessage("call-1", "ok", false),
		conversation.AssistantMessage(conversation.Text("done", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if got[1].Parts[0].Text != conversation.PortableReasoningUnavailableText {
		t.Fatalf(
			"backfilled reasoning text = %q, want %q",
			got[1].Parts[0].Text,
			conversation.PortableReasoningUnavailableText,
		)
	}
}

func TestStoreAppendTurnDoesNotBackfillReplayOnlyReasoningWithoutToolReplay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(conversation.Reasoning("", map[string]json.RawMessage{
			"openai_responses": json.RawMessage(`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`),
		})),
		conversation.AssistantMessage(conversation.Text("done", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	got, err := store.LoadRecentMessages(context.Background(), 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages() error = %v", err)
	}
	if got[1].Parts[0].Text != "" {
		t.Fatalf("reasoning text = %q, want empty", got[1].Parts[0].Text)
	}
}

func TestStoreLoadHead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	lastSeq, updatedAt, err := store.LoadHead(context.Background())
	if err != nil {
		t.Fatalf("LoadHead() error = %v", err)
	}
	if lastSeq != 0 {
		t.Fatalf("lastSeq = %d, want 0", lastSeq)
	}
	if updatedAt.IsZero() {
		t.Fatal("updatedAt = zero, want non-zero")
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(conversation.Text("ok", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	lastSeq, _, err = store.LoadHead(context.Background())
	if err != nil {
		t.Fatalf("LoadHead() after append error = %v", err)
	}
	if lastSeq != 1 {
		t.Fatalf("lastSeq after append = %d, want 1", lastSeq)
	}
}

func TestStoreStoreConsolidationCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(conversation.Text("ok", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	turn, ok, err := store.findTurnBySeq(1)
	if err != nil {
		t.Fatalf("findTurnBySeq() error = %v", err)
	}
	if !ok {
		t.Fatal("findTurnBySeq() ok = false, want true")
	}

	checkpoint, err := store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 1},
	)
	if err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() error = %v", err)
	}
	if committer.lastMessage != "memory: update consolidation checkpoint" {
		t.Fatalf("commit message = %q", committer.lastMessage)
	}
	if checkpoint.LastConsolidatedSeq != 1 {
		t.Fatalf("checkpoint.LastConsolidatedSeq = %d, want 1", checkpoint.LastConsolidatedSeq)
	}
	if checkpoint.LastConsolidatedTurnID != turn.ID {
		t.Fatalf(
			"checkpoint.LastConsolidatedTurnID = %q, want %q",
			checkpoint.LastConsolidatedTurnID,
			turn.ID,
		)
	}
	if !checkpoint.LastConsolidatedAt.Equal(turn.CreatedAt) {
		t.Fatalf(
			"checkpoint.LastConsolidatedAt = %s, want %s",
			checkpoint.LastConsolidatedAt,
			turn.CreatedAt,
		)
	}
	if checkpoint.UpdatedAt.IsZero() {
		t.Fatal("checkpoint.UpdatedAt = zero, want non-zero")
	}

	stored, err := store.readConsolidationCheckpoint()
	if err != nil {
		t.Fatalf("readConsolidationCheckpoint() error = %v", err)
	}
	if stored.LastConsolidatedSeq != checkpoint.LastConsolidatedSeq ||
		stored.LastConsolidatedTurnID != checkpoint.LastConsolidatedTurnID {
		t.Fatalf("stored checkpoint = %#v, want %#v", stored, checkpoint)
	}
	loaded, err := store.LoadConsolidationCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LoadConsolidationCheckpoint() error = %v", err)
	}
	if loaded.LastConsolidatedSeq != checkpoint.LastConsolidatedSeq ||
		loaded.LastConsolidatedTurnID != checkpoint.LastConsolidatedTurnID ||
		!loaded.LastConsolidatedAt.Equal(checkpoint.LastConsolidatedAt) {
		t.Fatalf("loaded checkpoint = %#v, want %#v", loaded, checkpoint)
	}

	commitCalls := committer.commitCalls
	checkpoint, err = store.StoreConsolidationCheckpoint(
		context.Background(),
		cognition.ConsolidationCheckpoint{LastConsolidatedSeq: 1},
	)
	if err != nil {
		t.Fatalf("StoreConsolidationCheckpoint() second error = %v", err)
	}
	if committer.commitCalls != commitCalls {
		t.Fatalf("commit calls = %d, want unchanged %d", committer.commitCalls, commitCalls)
	}
	if checkpoint.UpdatedAt.IsZero() {
		t.Fatal("checkpoint.UpdatedAt after no-op = zero, want existing timestamp")
	}
}

func TestStoreStoreSemanticExtractionCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := store.AppendTurn(context.Background(), []conversation.Message{
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(conversation.Text("ok", "")),
	}); err != nil {
		t.Fatalf("AppendTurn() error = %v", err)
	}

	turn, ok, err := store.findTurnBySeq(1)
	if err != nil {
		t.Fatalf("findTurnBySeq() error = %v", err)
	}
	if !ok {
		t.Fatal("findTurnBySeq() ok = false, want true")
	}

	checkpoint, err := store.StoreSemanticExtractionCheckpoint(
		context.Background(),
		cognition.SemanticExtractionCheckpoint{LastExtractedSeq: 1},
	)
	if err != nil {
		t.Fatalf("StoreSemanticExtractionCheckpoint() error = %v", err)
	}
	if committer.lastMessage != "memory: update semantic extraction checkpoint" {
		t.Fatalf("commit message = %q", committer.lastMessage)
	}
	if checkpoint.LastExtractedSeq != 1 {
		t.Fatalf("checkpoint.LastExtractedSeq = %d, want 1", checkpoint.LastExtractedSeq)
	}
	if checkpoint.LastExtractedTurnID != turn.ID {
		t.Fatalf(
			"checkpoint.LastExtractedTurnID = %q, want %q",
			checkpoint.LastExtractedTurnID,
			turn.ID,
		)
	}
	if !checkpoint.LastExtractedAt.Equal(turn.CreatedAt) {
		t.Fatalf(
			"checkpoint.LastExtractedAt = %s, want %s",
			checkpoint.LastExtractedAt,
			turn.CreatedAt,
		)
	}
	if checkpoint.UpdatedAt.IsZero() {
		t.Fatal("checkpoint.UpdatedAt = zero, want non-zero")
	}

	stored, err := store.readSemanticExtractionCheckpoint()
	if err != nil {
		t.Fatalf("readSemanticExtractionCheckpoint() error = %v", err)
	}
	if stored.LastExtractedSeq != checkpoint.LastExtractedSeq ||
		stored.LastExtractedTurnID != checkpoint.LastExtractedTurnID {
		t.Fatalf("stored checkpoint = %#v, want %#v", stored, checkpoint)
	}
	loaded, err := store.LoadSemanticExtractionCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LoadSemanticExtractionCheckpoint() error = %v", err)
	}
	if loaded.LastExtractedSeq != checkpoint.LastExtractedSeq ||
		loaded.LastExtractedTurnID != checkpoint.LastExtractedTurnID ||
		!loaded.LastExtractedAt.Equal(checkpoint.LastExtractedAt) {
		t.Fatalf("loaded checkpoint = %#v, want %#v", loaded, checkpoint)
	}

	commitCalls := committer.commitCalls
	checkpoint, err = store.StoreSemanticExtractionCheckpoint(
		context.Background(),
		cognition.SemanticExtractionCheckpoint{LastExtractedSeq: 1},
	)
	if err != nil {
		t.Fatalf("StoreSemanticExtractionCheckpoint() second error = %v", err)
	}
	if committer.commitCalls != commitCalls {
		t.Fatalf("commit calls = %d, want unchanged %d", committer.commitCalls, commitCalls)
	}
	if checkpoint.UpdatedAt.IsZero() {
		t.Fatal("checkpoint.UpdatedAt after no-op = zero, want existing timestamp")
	}
}

func TestStoreLoadSemanticExtractionCheckpointMissingFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := os.Remove(store.semanticExtractionCheckpointPath()); err != nil {
		t.Fatalf("Remove(semantic checkpoint) error = %v", err)
	}

	checkpoint, err := store.LoadSemanticExtractionCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LoadSemanticExtractionCheckpoint() error = %v", err)
	}
	if checkpoint != (cognition.SemanticExtractionCheckpoint{}) {
		t.Fatalf("checkpoint = %#v, want zero value", checkpoint)
	}
}

func TestStoreJobStateRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	state, err := store.LoadJobState(context.Background(), "working_memory.consolidate")
	if err != nil {
		t.Fatalf("LoadJobState() missing error = %v", err)
	}
	if state.LastObservedSeq != 0 {
		t.Fatalf("missing state = %#v, want zero value", state)
	}

	want := cognition.JobState{
		LastObservedSeq:     7,
		LastRunInputSeq:     7,
		LastSuccessInputSeq: 7,
		DirtySinceSeq:       8,
		DirtySinceAt:        time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC),
		LastScheduledFor: map[string]time.Time{
			"nightly": time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		},
	}
	if err := store.StoreJobState(
		context.Background(),
		"working_memory.consolidate",
		want,
	); err != nil {
		t.Fatalf("StoreJobState() error = %v", err)
	}
	if committer.lastMessage != "memory: update cognition trigger state working_memory_consolidate" {
		t.Fatalf("commit message = %q", committer.lastMessage)
	}

	got, err := store.LoadJobState(context.Background(), "working_memory.consolidate")
	if err != nil {
		t.Fatalf("LoadJobState() error = %v", err)
	}
	if got.LastObservedSeq != want.LastObservedSeq ||
		got.LastRunInputSeq != want.LastRunInputSeq ||
		got.DirtySinceSeq != want.DirtySinceSeq {
		t.Fatalf("loaded state = %#v, want %#v", got, want)
	}
	if got.LastScheduledFor["nightly"] != want.LastScheduledFor["nightly"] {
		t.Fatalf("LastScheduledFor = %#v, want %#v", got.LastScheduledFor, want.LastScheduledFor)
	}

	commitCalls := committer.commitCalls
	if err := store.StoreJobState(
		context.Background(),
		"working_memory.consolidate",
		want,
	); err != nil {
		t.Fatalf("StoreJobState() second error = %v", err)
	}
	if committer.commitCalls != commitCalls {
		t.Fatalf("commit calls = %d, want unchanged %d", committer.commitCalls, commitCalls)
	}
}

func TestStoreAppendRunRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	record := cognition.RunRecord{
		Type:       "verification.check",
		Cause:      cognition.RunCause{Kind: cognition.RunCauseSchedule, RuleID: "nightly"},
		StartedAt:  time.Date(2026, time.April, 6, 1, 2, 3, 456000000, time.UTC),
		FinishedAt: time.Date(2026, time.April, 6, 1, 2, 5, 0, time.UTC),
		InputSeq:   9,
		OutputSeq:  10,
		Succeeded:  true,
		Summary:    "ok",
		Metadata: map[string]string{
			"path":    cognition.VerificationReviewRuntimePath,
			"changed": "true",
		},
		ModelRef: "cognition",
		AttemptFailures: []cognition.AttemptFailure{
			{ModelRef: "gpt-5.4", Error: "openai failed"},
			{ModelRef: "glm-5-turbo", Error: "glm failed"},
		},
	}
	if err := store.AppendRunRecord(context.Background(), record); err != nil {
		t.Fatalf("AppendRunRecord() error = %v", err)
	}
	if committer.lastMessage != "memory: record cognition run verification_check" {
		t.Fatalf("commit message = %q", committer.lastMessage)
	}

	matches, err := filepath.Glob(filepath.Join(
		root,
		"cognition",
		"runs",
		"2026",
		"04",
		"06",
		"010203.456000000-verification_check.json",
	))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("run records = %#v, want one file", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile(run record) error = %v", err)
	}
	var got cognition.RunRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(run record) error = %v", err)
	}
	if got.Type != record.Type || got.Cause.RuleID != "nightly" || !got.Succeeded {
		t.Fatalf("run record = %#v", got)
	}
	if got.Metadata["path"] != cognition.VerificationReviewRuntimePath {
		t.Fatalf("run metadata = %#v", got.Metadata)
	}
	if len(got.AttemptFailures) != 2 {
		t.Fatalf("attempt failures = %#v, want two entries", got.AttemptFailures)
	}
	if got.AttemptFailures[0].ModelRef != "gpt-5.4" ||
		got.AttemptFailures[1].ModelRef != "glm-5-turbo" {
		t.Fatalf("attempt failures = %#v", got.AttemptFailures)
	}
}

func TestStoreLoadAndStoreCognitionArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := NewStore(root, "Jared", committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	got, err := store.LoadCognitionArtifact(context.Background(), cognition.VerificationReviewPath)
	if err != nil {
		t.Fatalf("LoadCognitionArtifact() missing error = %v", err)
	}
	if got != (cognition.Artifact{}) {
		t.Fatalf("missing artifact = %#v, want zero value", got)
	}

	artifact := cognition.Artifact{
		RelativePath: cognition.VerificationReviewPath,
		Content:      "# Verification Review\n\n- Check stale assumptions.",
	}
	if err := store.StoreCognitionArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("StoreCognitionArtifact() error = %v", err)
	}
	if committer.lastMessage != "memory: update cognition artifact "+cognition.VerificationReviewPath {
		t.Fatalf("commit message = %q", committer.lastMessage)
	}

	got, err = store.LoadCognitionArtifact(context.Background(), artifact.RelativePath)
	if err != nil {
		t.Fatalf("LoadCognitionArtifact() error = %v", err)
	}
	if got.RelativePath != artifact.RelativePath || got.Content != artifact.Content {
		t.Fatalf("artifact = %#v, want %#v", got, artifact)
	}

	commitCalls := committer.commitCalls
	if err := store.StoreCognitionArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("StoreCognitionArtifact() second error = %v", err)
	}
	if committer.commitCalls != commitCalls {
		t.Fatalf("commit calls = %d, want unchanged %d", committer.commitCalls, commitCalls)
	}
}

func TestStoreCognitionArtifactRejectsPathEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := NewStore(root, "Jared", &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if _, err := store.LoadCognitionArtifact(context.Background(), "../escape.md"); err == nil {
		t.Fatal("LoadCognitionArtifact() error = nil, want path validation failure")
	}
	if err := store.StoreCognitionArtifact(context.Background(), cognition.Artifact{
		RelativePath: "../escape.md",
		Content:      "bad",
	}); err == nil {
		t.Fatal("StoreCognitionArtifact() error = nil, want path validation failure")
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
