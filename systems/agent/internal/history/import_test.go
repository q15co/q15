package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func sourceFixture(t *testing.T, turns []sourceTurn, head int64) string {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"history/turns", "history/state"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, value any) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("history/state/head.json", map[string]any{"last_seq": head})
	for _, turn := range turns {
		write(filepath.Join("history/turns", time.Unix(turn.Seq, 0).Format("150405")+".json"), turn)
	}
	return root
}

func TestImportVerifiesEveryTurnAndPreservesIdentityHoles(t *testing.T) {
	store := testStore(t)
	clock := time.Date(2026, 10, 24, 10, 0, 0, 123456789, time.FixedZone("", 7200))
	turns := []sourceTurn{
		{
			SchemaVersion: conversation.SchemaVersion,
			Seq:           10001,
			CreatedAt:     clock,
			Messages: []conversation.Message{
				{
					Role:         conversation.UserRole,
					UserTemporal: &conversation.UserTemporalMetadata{TimeLocal: clock},
					Parts:        []conversation.Part{conversation.Text("ok", "")},
				},
			},
		},
		{
			SchemaVersion: conversation.SchemaVersion,
			Seq:           10003,
			CreatedAt:     clock.Add(time.Second),
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall("retry", "read", "malformed {"),
				),
				{
					Role: conversation.ToolRole,
					Parts: []conversation.Part{
						conversation.ToolResult("retry", "binary\x00output", true),
					},
				},
			},
		},
	}
	root := sourceFixture(t, turns, 10005)
	for attempt := range 2 {
		report, err := store.Import(t.Context(), root, false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Mismatches != 0 || report.SourceTurns != 2 {
			t.Fatalf("unexpected report: %+v", report)
		}
		if attempt == 0 && report.Imported != 2 {
			t.Fatalf("initial import: %+v", report)
		}
		if attempt == 1 && report.Unchanged != 2 {
			t.Fatalf("repeat import: %+v", report)
		}
	}
	report, err := store.Import(t.Context(), root, true)
	if err != nil || report.Mismatches != 0 || report.Imported != 0 {
		t.Fatalf("verify: %+v, %v", report, err)
	}
	id, err := store.ReserveTurnSeq(t.Context())
	if err != nil || id <= 10005 {
		t.Fatalf("identity reused an imported reservation: %d, %v", id, err)
	}
	turns[0].Messages[0].Parts[0].Text = "changed source"
	changed := sourceFixture(t, turns, 10005)
	if report, err := store.Import(t.Context(), changed, true); err == nil ||
		report.Mismatches != 1 {
		t.Fatalf("conflict was not reported: %+v, %v", report, err)
	}
}

func TestJSONComparisonPreservesNumericAndInvalidArgumentSemantics(t *testing.T) {
	if !equalJSON([]byte(`{"n":1e3,"x":[true,null]}`), []byte(`{"x":[true,null],"n":1000.0}`)) {
		t.Fatal("equivalent jsonb values differ")
	}
	if equalJSON([]byte(`null invalid`), []byte(`null other`)) {
		t.Fatal("malformed arguments were treated as valid JSON")
	}
	if equalJSON([]byte(`{"n":9007199254740992}`), []byte(`{"n":9007199254740993}`)) {
		t.Fatal("integer precision was lost")
	}
}

// TestImportFrozenSnapshot exercises real, private operator snapshots without committing their contents.
func TestImportFrozenSnapshot(t *testing.T) {
	root := os.Getenv("Q15_IMPORT_SNAPSHOT_ROOT")
	if root == "" {
		t.Skip("set Q15_IMPORT_SNAPSHOT_ROOT to a private frozen legacy snapshot")
	}
	store := testStore(t)
	for attempt := range 2 {
		started := time.Now()
		report, err := store.Import(t.Context(), root, false)
		if err != nil {
			t.Fatalf("snapshot import attempt %d: %v", attempt+1, err)
		}
		if report.SourceTurns == 0 || report.Mismatches != 0 {
			t.Fatalf("invalid import report: %+v", report)
		}
		t.Logf(
			"attempt=%d source_turns=%d imported=%d unchanged=%d elapsed=%s",
			attempt+1,
			report.SourceTurns,
			report.Imported,
			report.Unchanged,
			time.Since(started),
		)
	}
	readonly, err := OpenReadOnly(t.Context(), os.Getenv("Q15_TEST_POSTGRES_URL"), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err := readonly.Import(t.Context(), root, true); err != nil {
		t.Fatal(err)
	}
}
