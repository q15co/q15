package memory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

// fixtureTurnDate is the synthetic created_at every fixture turn carries, so
// paging assertions have a timestamp to compare against.
const fixtureTurnDate = "2026-04-08T12:30:00.123456789Z"

// fixtureTurnJSON renders one synthetic turn fixture in the verified
// transcript shape, authored here rather than captured from a real memory
// root: this repository is public and the transcripts it talks to are not.
func fixtureTurnJSON(seq int64) string {
	return fmt.Sprintf(`{
  "schema_version": 5,
  "id": "turn-%020d",
  "seq": %d,
  "created_at": "`+fixtureTurnDate+`",
  "messages": [
    {"role": "user", "parts": [{"type": "text", "text": "fixture user message %d"}]},
    {"role": "assistant", "parts": [{"type": "text", "text": "fixture assistant message %d", "disposition": "final"}]}
  ]
}
`, seq, seq, seq, seq)
}

const fixtureRichTurnJSON = `{
  "schema_version": 5,
  "id": "turn-00000000000000000010",
  "seq": 10,
  "created_at": "` + fixtureTurnDate + `",
  "messages": [
    {"role": "user", "parts": [{"type": "text", "text": "fixture user message"}]},
    {"role": "assistant", "parts": [
      {"type": "text", "text": "fixture commentary", "disposition": "commentary"},
      {"type": "reasoning", "text": "fixture reasoning", "disposition": "final"},
      {"type": "media", "media_kind": "image", "media_ref": "media://fixture/sha256/abc"},
      {"type": "tool_call", "id": "call-1", "name": "fixture_tool", "arguments": "{\"path\":\"fixture\"}"}
    ]},
    {"role": "tool", "parts": [
      {"type": "tool_result", "tool_call_id": "call-1", "content": "fixture content", "is_error": true}
    ]}
  ]
}
`

func writeFixtureTurn(t *testing.T, root string, seq int64, body string) {
	t.Helper()
	path := filepath.Join(
		root, "history", "turns", "2026", "04", "08", fmt.Sprintf("%020d.json", seq),
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func writeFixtureTurns(t *testing.T, root string, seqs []int64) {
	t.Helper()
	for _, seq := range seqs {
		writeFixtureTurn(t, root, seq, fixtureTurnJSON(seq))
	}
}

func writeFixtureHead(t *testing.T, root string, lastSeq int64) {
	t.Helper()
	path := filepath.Join(root, "history", "state", "head.json")
	body := fmt.Sprintf(
		`{"last_seq": %d, "updated_at": "2026-04-08T12:31:00Z"}`+"\n", lastSeq,
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func newFixtureStore(root string) *Store {
	return NewStore(memoryrepo.New(root, &fakeCommitter{}), "Jared")
}

// seqRange returns every turn sequence from start up to end, oldest to newest.
func seqRange(start, end int64) []int64 {
	seqs := make([]int64, 0, int(end-start)+1)
	for seq := start; seq <= end; seq++ {
		seqs = append(seqs, seq)
	}
	return seqs
}

// seqRangeDesc returns every turn sequence from start up to end, newest to
// oldest, matching the page order ListTurns promises.
func seqRangeDesc(start, end int64) []int64 {
	seqs := make([]int64, 0, int(end-start)+1)
	for seq := end; seq >= start; seq-- {
		seqs = append(seqs, seq)
	}
	return seqs
}

func TestStoreListTurns(t *testing.T) {
	tests := []struct {
		name        string
		headSeq     int64
		turns       []int64
		makeTurnDir bool
		afterSeq    int64
		limit       int
		wantSeqs    []int64
		wantHasMore bool
	}{
		{
			name:     "newest first through the whole transcript",
			headSeq:  12,
			turns:    []int64{10, 12, 11},
			afterSeq: 0,
			limit:    0,
			wantSeqs: []int64{12, 11, 10},
		},
		{
			name:     "after_seq starts strictly older so clients page backwards",
			headSeq:  12,
			turns:    []int64{10, 11, 12},
			afterSeq: 12,
			limit:    2,
			wantSeqs: []int64{11, 10},
		},
		{
			name:        "default limit holds fifty turns",
			headSeq:     52,
			turns:       seqRange(1, 52),
			afterSeq:    0,
			limit:       0,
			wantSeqs:    seqRangeDesc(3, 52),
			wantHasMore: true,
		},
		{
			name:        "limit is capped at the maximum",
			headSeq:     505,
			turns:       seqRange(1, 505),
			afterSeq:    0,
			limit:       600,
			wantSeqs:    seqRangeDesc(6, 505),
			wantHasMore: true,
		},
		{
			name:        "one turn a page still starts at the newest",
			headSeq:     12,
			turns:       []int64{10, 11, 12},
			afterSeq:    0,
			limit:       1,
			wantSeqs:    []int64{12},
			wantHasMore: true,
		},
		{
			name:     "no turns are strictly older than after_seq",
			headSeq:  12,
			turns:    []int64{10, 11, 12},
			afterSeq: 10,
			limit:    0,
			wantSeqs: []int64{},
		},
		{
			name:     "missing turns directory is an empty page",
			headSeq:  4,
			afterSeq: 0,
			limit:    0,
			wantSeqs: []int64{},
		},
		{
			name:        "empty turns directory is an empty page",
			headSeq:     0,
			makeTurnDir: true,
			afterSeq:    0,
			limit:       0,
			wantSeqs:    []int64{},
		},
		{
			name:     "turn files above head are ignored",
			headSeq:  12,
			turns:    []int64{10, 11, 12, 14},
			afterSeq: 0,
			limit:    0,
			wantSeqs: []int64{12, 11, 10},
		},
		{
			name:     "head_seq may sit above the newest turn file",
			headSeq:  13,
			turns:    []int64{10, 11, 12},
			afterSeq: 0,
			limit:    0,
			wantSeqs: []int64{12, 11, 10},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureHead(t, root, tc.headSeq)
			if tc.makeTurnDir {
				if err := os.MkdirAll(
					filepath.Join(root, "history", "turns"), 0o755,
				); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			}
			writeFixtureTurns(t, root, tc.turns)
			store := newFixtureStore(root)

			page, err := store.ListTurns(context.Background(), tc.afterSeq, tc.limit)
			if err != nil {
				t.Fatalf("ListTurns() error = %v", err)
			}

			gotSeqs := make([]int64, 0, len(page.Turns))
			for _, turn := range page.Turns {
				gotSeqs = append(gotSeqs, turn.Seq)
			}
			if !equalSeqs(gotSeqs, tc.wantSeqs) {
				t.Fatalf("ListTurns() turn seqs = %v, want %v", gotSeqs, tc.wantSeqs)
			}
			if page.HeadSeq != tc.headSeq {
				t.Fatalf("ListTurns() head seq = %d, want %d", page.HeadSeq, tc.headSeq)
			}
			if page.HasMore != tc.wantHasMore {
				t.Fatalf("ListTurns() has more = %v, want %v", page.HasMore, tc.wantHasMore)
			}
		})
	}
}

func equalSeqs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestStoreListTurnsReturnsTurnVerbatim(t *testing.T) {
	root := t.TempDir()
	writeFixtureHead(t, root, 10)
	writeFixtureTurn(t, root, 10, fixtureRichTurnJSON)
	store := newFixtureStore(root)

	page, err := store.ListTurns(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	if len(page.Turns) != 1 {
		t.Fatalf("ListTurns() turns = %d, want 1", len(page.Turns))
	}
	turn := page.Turns[0]
	if turn.Seq != 10 {
		t.Fatalf("Turn seq = %d, want 10", turn.Seq)
	}
	wantCreatedAt := time.Date(2026, time.April, 8, 12, 30, 0, 123456789, time.UTC)
	if !turn.CreatedAt.Equal(wantCreatedAt) {
		t.Fatalf("Turn created at = %v, want %v", turn.CreatedAt, wantCreatedAt)
	}

	// The pager hands readers the durable turn, not a prompt-visible replay
	// slice: messages come back verbatim, tool messages included.
	if len(turn.Messages) != 3 {
		t.Fatalf("Turn messages = %d, want 3", len(turn.Messages))
	}
	user := turn.Messages[0]
	if user.Role != conversation.UserRole {
		t.Fatalf("first message role = %q, want user", user.Role)
	}
	if len(user.Parts) != 1 || user.Parts[0].Text != "fixture user message" {
		t.Fatalf("first message parts = %#v, want one user text part", user.Parts)
	}

	assistant := turn.Messages[1]
	if assistant.Role != conversation.AssistantRole {
		t.Fatalf("second message role = %q, want assistant", assistant.Role)
	}
	if len(assistant.Parts) != 4 {
		t.Fatalf("assistant parts = %d, want 4", len(assistant.Parts))
	}
	text := assistant.Parts[0]
	if text.Type != conversation.TextPartType ||
		text.Text != "fixture commentary" ||
		text.Disposition != conversation.TextDispositionCommentary {
		t.Fatalf("text part = %#v, want commentary text", text)
	}
	reasoning := assistant.Parts[1]
	if reasoning.Type != conversation.ReasoningPartType ||
		reasoning.Text != "fixture reasoning" {
		t.Fatalf("reasoning part = %#v, want reasoning text", reasoning)
	}
	media := assistant.Parts[2]
	if media.Type != conversation.MediaPartType ||
		media.MediaKind != conversation.MediaKindImage ||
		media.MediaRef != "media://fixture/sha256/abc" {
		t.Fatalf("media part = %#v, want image media ref", media)
	}
	call := assistant.Parts[3]
	if call.Type != conversation.ToolCallPartType ||
		call.ID != "call-1" || call.Name != "fixture_tool" ||
		call.Arguments != `{"path":"fixture"}` {
		t.Fatalf("tool call part = %#v, want tool call", call)
	}

	tool := turn.Messages[2]
	if tool.Role != conversation.ToolRole || len(tool.Parts) != 1 {
		t.Fatalf("third message = %#v, want one tool message", tool)
	}
	result := tool.Parts[0]
	if result.Type != conversation.ToolResultPartType ||
		result.ToolCallID != "call-1" || result.Content != "fixture content" ||
		!result.IsError {
		t.Fatalf("tool result part = %#v, want failed tool result", result)
	}
}

func TestStoreListTurnsIsReadOnly(t *testing.T) {
	root := t.TempDir()
	writeFixtureHead(t, root, 12)
	writeFixtureTurns(t, root, []int64{10, 11, 12})
	committer := &fakeCommitter{}
	store := &Store{
		repository: memoryrepo.New(root, committer),
		agentName:  "Jared",
	}

	before := snapshotFixturePaths(t, root)
	page, err := store.ListTurns(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	if len(page.Turns) != 3 {
		t.Fatalf("ListTurns() turns = %d, want 3", len(page.Turns))
	}

	if committer.ensureCalls != 0 || committer.commitCalls != 0 {
		t.Fatalf(
			"ListTurns() touched the repository committer (ensure=%d commit=%d)",
			committer.ensureCalls,
			committer.commitCalls,
		)
	}
	after := snapshotFixturePaths(t, root)
	if strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf(
			"ListTurns() changed the repository paths\nbefore=%v\nafter=%v",
			before,
			after,
		)
	}
}

// snapshotFixturePaths records every path under root, files and directories,
// so tests can prove the read path left the repository untouched.
func snapshotFixturePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%q) error = %v", root, err)
	}
	return paths
}
