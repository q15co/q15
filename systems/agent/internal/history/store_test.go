package history

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("Q15_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("run make test-history-postgres for database integration coverage")
	}
	store, err := Open(t.Context(), dsn, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestStoreRelationalRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	clock := time.Date(2026, 10, 24, 12, 30, 0, 123000, time.FixedZone("", 7200))
	messages := []conversation.Message{
		{
			Role: conversation.UserRole,
			UserTemporal: &conversation.UserTemporalMetadata{
				TimeLocal:            clock,
				SincePrevUserMessage: conversation.NewDuration(17 * time.Second),
			},
			Parts: []conversation.Part{
				conversation.Text("ok", ""),
				conversation.Media(conversation.MediaKindAnimation, "media:test-animation"),
				conversation.Image("", "data:image/png;base64,AA=="),
				conversation.Image("media:test-image", "data:image/png;base64,AQ=="),
			},
		},
		{
			Role: conversation.AssistantRole,
			Parts: []conversation.Part{
				{
					Type: conversation.ReasoningPartType,
					Text: "portable reasoning",
					Replay: map[string]json.RawMessage{
						"provider": json.RawMessage(`{"signature":"opaque"}`),
					},
				},
				conversation.ToolCall("call-1", "read", `{"path":"a"}`),
			},
		},
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "original\x00output", false),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", conversation.TextDispositionFinal)),
	}
	id, err := store.ReserveTurnSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListTurns(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Turns) != 0 || page.HeadSeq != id {
		t.Fatalf("reserved turn leaked: %+v", page)
	}
	if err := store.AppendTurnAtSeq(ctx, id, messages); err != nil {
		t.Fatal(err)
	}
	turn, err := store.ReadTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := conversation.NormalizeMessages(messages)
	if !equivalentTurn(turn, sourceTurn{Seq: id, CreatedAt: turn.CreatedAt, Messages: want}) {
		t.Fatalf("round trip mismatch:\ngot %#v\nwant %#v", turn.Messages, want)
	}
	last, ok, err := store.LoadLastUserTimestamp(ctx)
	if err != nil || !ok || !last.Equal(clock.Truncate(time.Second)) {
		t.Fatalf("last clock = %v, %v, %v", last, ok, err)
	}
	_, offset := last.Zone()
	if offset != 7200 {
		t.Fatalf("lost original offset: %d", offset)
	}
	if err := store.AppendTurnAtSeq(ctx, id, messages); err == nil {
		t.Fatal("completed turn was overwritten")
	}
	if _, err := store.pool.Exec(ctx, "UPDATE memory.part_tool_results SET body = 'replacement', raw_body = NULL WHERE part_id IN (SELECT p.id FROM memory.parts p JOIN memory.messages m ON m.id=p.message_id WHERE m.turn_id=$1)", id); err == nil {
		t.Fatal("database allowed a completed payload update")
	}
	if err := store.DeleteTurn(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadTurn(ctx, id); err != pgx.ErrNoRows {
		t.Fatalf("deleted turn returned %v", err)
	}
}

func TestStoreEnforcesPartAndClockConstraints(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	id, err := store.ReserveTurnSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q := historydb.New(tx)
	messageID, err := q.InsertMessage(
		ctx,
		historydb.InsertMessageParams{
			TurnID:   id,
			Position: 0,
			Role:     historydb.MemoryMessageRoleAssistant,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertPart(ctx, historydb.InsertPartParams{MessageID: messageID, Position: 0, Kind: historydb.MemoryPartKindText}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("database accepted a part without a payload")
	}
	tx, err = store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q = historydb.New(tx)
	messageID, err = q.InsertMessage(
		ctx,
		historydb.InsertMessageParams{
			TurnID:   id,
			Position: 0,
			Role:     historydb.MemoryMessageRoleAssistant,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertClock(ctx, historydb.InsertClockParams{MessageID: messageID, Instant: timestamp(time.Now())}); err == nil {
		t.Fatal("database accepted a clock on an assistant message")
	}
}

func TestJSONPayloadExceptions(t *testing.T) {
	for _, input := range []string{"", "malformed {", `{"value":"\u0000"}`} {
		structured, raw := splitJSON([]byte(input))
		if structured != nil || string(raw) != input {
			t.Fatalf("exceptional JSON payload changed: %q", input)
		}
	}
	structured, raw := splitJSON([]byte(`{"value":42}`))
	if raw != nil || string(structured) != `{"value":42}` {
		t.Fatal("valid JSON was not stored as jsonb")
	}
}
