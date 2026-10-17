package memory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func TestMemoryRecoveryRepeatedLedgerRotationsPreserveHistory(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	ctx := context.Background()
	var expected []byte
	for run := range 5 {
		snapshots := recoveryTestSnapshots(t, store)
		id, err := store.BeginMemoryRecovery(ctx, snapshots, semanticTopicsRelativePath)
		if err != nil {
			t.Fatal(err)
		}
		entry := claims.FormatLedgerEntry(claims.LedgerEntry{
			Run: "semantic_memory.extract", SourceSeq: fmt.Sprintf("t%d", run+1),
			Outcome: claims.OutcomeDropped, Description: strings.Repeat("x", 1024), Note: "obsolete",
		})
		batch := []byte(strings.Repeat(entry, 128))
		expected = append(expected, batch...)
		raw := append(snapshots["/memory/"+workingLedgerRelativePath].Content, batch...)
		if err := store.WriteFileBytes(ctx, workingLedgerRelativePath, raw); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteMemoryRecovery(ctx, id); err != nil {
			t.Fatal(err)
		}
		history, err := store.LoadConversionLedgerHistory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		active, _, err := store.ReadFileBytes(ctx, workingLedgerRelativePath)
		if err != nil || len(active) >= ledgerRotationThresholdByte {
			t.Fatalf("run %d: active ledger=%d bytes, error=%v", run, len(active), err)
		}
		got, err := claims.ParseLedger([]byte(history + "\n" + string(active)))
		want, wantErr := claims.ParseLedger(expected)
		if err != nil || wantErr != nil || len(got) != len(want) {
			t.Fatalf(
				"run %d: ledger entries=%d want=%d errors=%v, %v",
				run,
				len(got),
				len(want),
				err,
				wantErr,
			)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("run %d: ledger entry %d changed or moved", run, i)
			}
		}
		assertRecoveryGitClean(t, root)
	}
}

func TestMemoryRecoveryRotatesLedgerAtThreshold(t *testing.T) {
	for _, size := range []int{
		ledgerRotationThresholdByte - 1,
		ledgerRotationThresholdByte,
		ledgerRotationThresholdByte + 1,
	} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			root, store, _ := newRecoveryTestStore(t)
			ctx := context.Background()
			id, err := store.BeginMemoryRecovery(
				ctx,
				recoveryTestSnapshots(t, store),
				semanticTopicsRelativePath,
			)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(strings.Repeat("\n", size))
			if err := store.WriteFileBytes(ctx, workingLedgerRelativePath, raw); err != nil {
				t.Fatal(err)
			}
			before, _, err := store.ReadFileBytes(ctx, workingLedgerRelativePath)
			if err != nil || !bytes.Equal(before, raw) {
				t.Fatalf("ledger changed before validation: %v", err)
			}
			if err := store.CompleteMemoryRecovery(ctx, id); err != nil {
				t.Fatal(err)
			}
			history, err := store.LoadConversionLedgerHistory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			active, _, err := store.ReadFileBytes(ctx, workingLedgerRelativePath)
			if err != nil {
				t.Fatal(err)
			}
			if size < ledgerRotationThresholdByte {
				if history != "" || !bytes.Equal(active, raw) {
					t.Fatal("ledger rotated below the threshold")
				}
			} else if history != string(raw) || len(active) >= ledgerRotationThresholdByte ||
				!bytes.Contains(active, []byte("# Conversion Ledger")) {
				t.Fatalf("ledger did not rotate: history=%d bytes active=%d bytes", len(history), len(active))
			}
			assertRecoveryGitClean(t, root)
		})
	}
}

func TestMemoryRecoveryRestoresLedgerRotationAfterCommitFailure(t *testing.T) {
	for _, existingArchive := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_archive=%v", existingArchive), func(t *testing.T) {
			root, store, committer := newRecoveryTestStore(t)
			ctx := context.Background()
			archivePath := store.ledgerRotationTarget(time.Now().UTC())
			const older = "# Previously archived ledger\n"
			if existingArchive {
				if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := store.WriteFileBytes(ctx, ledgerDirPath+"/"+filepath.Base(archivePath), []byte(older)); err != nil {
					t.Fatal(err)
				}
			}
			snapshots := recoveryTestSnapshots(t, store)
			id, err := store.BeginMemoryRecovery(ctx, snapshots, semanticTopicsRelativePath)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(strings.Repeat("\n", ledgerRotationThresholdByte))
			if err := store.WriteFileBytes(ctx, workingLedgerRelativePath, raw); err != nil {
				t.Fatal(err)
			}
			committer.fail = true
			if err := store.CompleteMemoryRecovery(ctx, id); err == nil ||
				!strings.Contains(err.Error(), "injected Git failure") {
				t.Fatalf("CompleteMemoryRecovery() error = %v, want Git failure", err)
			}
			archived, err := os.ReadFile(archivePath)
			if err != nil || !bytes.HasSuffix(archived, raw) {
				t.Fatalf("ledger was not archived before the failed commit: %v", err)
			}
			committer.fail = false
			if err := store.CompleteMemoryRecovery(ctx, id); err == nil ||
				!strings.Contains(err.Error(), "rotation is pending") {
				t.Fatalf("completion retried an interrupted rotation: %v", err)
			}
			fresh := newTestStore(root, committer)
			if err := fresh.Init(ctx); err != nil {
				t.Fatal(err)
			}
			assertRecoveryFiles(t, fresh, snapshots)
			archived, err = os.ReadFile(archivePath)
			if existingArchive {
				if err != nil || string(archived) != older {
					t.Fatalf("existing archive was not restored: %q, %v", archived, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed run's archive survived recovery: %v", err)
			}
			assertRecoveryGitClean(t, root)
		})
	}
}
