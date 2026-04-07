package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
		filepath.Join(root, "working"),
		filepath.Join(root, "working", "WORKING_MEMORY.md"),
		filepath.Join(root, "history", "turns"),
		filepath.Join(root, "history", "state", "head.json"),
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
		"Semantic memory is stored under semantic/",
		"working/WORKING_MEMORY.md",
		"notes/ is never working memory",
		"history/state/head.json",
		"cognition/",
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
		ModelRef:   "cognition",
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
