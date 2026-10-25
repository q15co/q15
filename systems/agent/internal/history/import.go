package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

// ImportReport reports exact source-turn verification, without transcript content.
type ImportReport struct {
	SourceTurns int    `json:"source_turns"`
	Imported    int    `json:"imported"`
	Unchanged   int    `json:"unchanged"`
	Mismatches  int    `json:"mismatches"`
	SourceHead  int64  `json:"source_head"`
	Fingerprint string `json:"fingerprint"`
}

type sourceTurn struct {
	SchemaVersion int                    `json:"schema_version"`
	Seq           int64                  `json:"seq"`
	CreatedAt     time.Time              `json:"created_at"`
	Messages      []conversation.Message `json:"messages"`
}

type sourceEntry struct {
	path string
	id   int64
	hash [32]byte
}

type sourceSnapshot struct {
	entries     []sourceEntry
	head        int64
	fingerprint [32]byte
}

// Import imports or verifies a frozen file-store snapshot. Existing turns must
// match; importing never updates or repairs a conflicting database row.
func (s *Store) Import(ctx context.Context, root string, verifyOnly bool) (ImportReport, error) {
	source, err := snapshotSource(ctx, root)
	if err != nil {
		return ImportReport{}, err
	}
	report := ImportReport{
		SourceTurns: len(source.entries),
		SourceHead:  source.head,
		Fingerprint: fmt.Sprintf("%x", source.fingerprint),
	}
	for _, entry := range source.entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		turn, raw, err := readSource(entry.path)
		if err != nil {
			return report, err
		}
		if sha256.Sum256(raw) != entry.hash {
			return report, fmt.Errorf("source turn %d changed during import", entry.id)
		}
		if !verifyOnly {
			inserted, err := s.importTurn(ctx, turn)
			if err != nil {
				return report, fmt.Errorf("import source turn %d: %w", entry.id, err)
			}
			if inserted {
				report.Imported++
			} else {
				report.Unchanged++
			}
		}
		stored, err := s.ReadTurn(ctx, entry.id)
		if err != nil || !equivalentTurn(stored, turn) {
			report.Mismatches++
			if err != nil {
				return report, fmt.Errorf("verify source turn %d: %w", entry.id, err)
			}
			return report, fmt.Errorf("source turn %d differs from the database", entry.id)
		}
	}
	count, err := historydb.New(s.pool).CountTurns(ctx, s.agentID)
	if err != nil {
		return report, err
	}
	if count != int64(len(source.entries)) {
		report.Mismatches++
		return report, fmt.Errorf(
			"database has %d completed turns; source has %d",
			count,
			len(source.entries),
		)
	}
	after, err := snapshotSource(ctx, root)
	if err != nil {
		return report, err
	}
	if source.fingerprint != after.fingerprint {
		return report, fmt.Errorf("source history changed during import; freeze it and rerun")
	}
	if !verifyOnly && source.head > 0 {
		if _, err := historydb.New(s.pool).AdvanceTurnIdentity(ctx, source.head); err != nil {
			return report, fmt.Errorf("advance imported turn identity: %w", err)
		}
	}
	if !verifyOnly {
		if err := historydb.New(s.pool).RecordImport(ctx, historydb.RecordImportParams{
			AgentID: s.agentID, SourceFingerprint: source.fingerprint[:], SourceHead: source.head, SourceTurns: int64(len(source.entries)),
		}); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (s *Store) importTurn(ctx context.Context, turn sourceTurn) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	inserted, err := q.InsertImportedTurn(
		ctx,
		historydb.InsertImportedTurnParams{
			ID:        turn.Seq,
			AgentID:   s.agentID,
			CreatedAt: timestamp(turn.CreatedAt),
		},
	)
	if err != nil || inserted == 0 {
		return false, err
	}
	if err := writeMessages(ctx, q, turn.Seq, turn.Messages); err != nil {
		return false, err
	}
	if _, err := q.CompleteTurn(ctx, historydb.CompleteTurnParams{ID: turn.Seq, AgentID: s.agentID}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func snapshotSource(ctx context.Context, root string) (sourceSnapshot, error) {
	var snapshot sourceSnapshot
	head, err := os.ReadFile(filepath.Join(root, "history", "state", "head.json"))
	if err != nil {
		return snapshot, fmt.Errorf("read source history head: %w", err)
	}
	var state struct {
		LastSeq int64 `json:"last_seq"`
	}
	if err := json.Unmarshal(head, &state); err != nil {
		return snapshot, fmt.Errorf("decode source history head: %w", err)
	}
	snapshot.head = state.LastSeq
	seen := make(map[int64]bool)
	err = filepath.WalkDir(
		filepath.Join(root, "history", "turns"),
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() || filepath.Ext(path) != ".json" {
				return fmt.Errorf("source history contains an unsupported entry")
			}
			turn, raw, err := readSource(path)
			if err != nil {
				return err
			}
			if turn.Seq > state.LastSeq {
				return fmt.Errorf("source turn %d is above its durable head", turn.Seq)
			}
			if seen[turn.Seq] {
				return fmt.Errorf("source turn %d occurs more than once", turn.Seq)
			}
			seen[turn.Seq] = true
			snapshot.entries = append(
				snapshot.entries,
				sourceEntry{path: path, id: turn.Seq, hash: sha256.Sum256(raw)},
			)
			return nil
		},
	)
	if err != nil {
		return snapshot, err
	}
	slices.SortFunc(snapshot.entries, func(a, b sourceEntry) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	hash := sha256.New()
	fmt.Fprintf(hash, "head=%d\n", snapshot.head)
	for _, entry := range snapshot.entries {
		fmt.Fprintf(hash, "%d:%x\n", entry.id, entry.hash)
	}
	copy(snapshot.fingerprint[:], hash.Sum(nil))
	return snapshot, nil
}

func readSource(path string) (sourceTurn, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sourceTurn{}, nil, fmt.Errorf("read source turn: %w", err)
	}
	var turn sourceTurn
	if err := json.Unmarshal(raw, &turn); err != nil {
		return turn, nil, fmt.Errorf("decode source turn: %w", err)
	}
	if turn.SchemaVersion != conversation.SchemaVersion || turn.Seq <= 0 ||
		turn.CreatedAt.IsZero() ||
		len(turn.Messages) == 0 {
		return turn, nil, fmt.Errorf(
			"source turn %d has an unsupported schema or incomplete metadata",
			turn.Seq,
		)
	}
	return turn, raw, nil
}

func equivalentTurn(stored conversation.Turn, source sourceTurn) bool {
	if stored.Seq != source.Seq ||
		!stored.CreatedAt.Equal(source.CreatedAt.Truncate(time.Microsecond)) {
		return false
	}
	actual, expected := conversation.CloneMessages(
		stored.Messages,
	), conversation.CloneMessages(
		source.Messages,
	)
	if len(actual) != len(expected) {
		return false
	}
	for i := range expected {
		if len(actual[i].Parts) != len(expected[i].Parts) {
			return false
		}
		for _, message := range []*conversation.Message{&actual[i], &expected[i]} {
			if message.UserTemporal != nil {
				message.UserTemporal.TimeLocal = message.UserTemporal.TimeLocal.Truncate(
					time.Microsecond,
				)
			}
			if message.ExternalEvent != nil {
				message.ExternalEvent.DeliveredAt = message.ExternalEvent.DeliveredAt.Truncate(
					time.Microsecond,
				)
			}
		}
		for j := range expected[i].Parts {
			a, b := &actual[i].Parts[j], &expected[i].Parts[j]
			if a.Type == conversation.ToolCallPartType {
				if a.Arguments != b.Arguments &&
					!equalJSON([]byte(a.Arguments), []byte(b.Arguments)) {
					return false
				}
				a.Arguments, b.Arguments = "", ""
			}
		}
	}
	a, err := json.Marshal(actual)
	if err != nil {
		return false
	}
	b, err := json.Marshal(expected)
	return err == nil && equalJSON(a, b)
}

func equalJSON(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	if !json.Valid(a) || !json.Valid(b) {
		return false
	}
	var actual, expected any
	for _, item := range []struct {
		raw         []byte
		destination *any
	}{{a, &actual}, {b, &expected}} {
		decoder := json.NewDecoder(bytes.NewReader(item.raw))
		decoder.UseNumber()
		if err := decoder.Decode(item.destination); err != nil {
			return false
		}
	}
	return equalJSONValue(actual, expected)
}
