package cognition

import (
	"context"
	"strings"
	"testing"
)

type repairPathProbeLoader struct {
	*repairProbeLoader
	reads []string
}

func (l *repairPathProbeLoader) ReadFileBytes(
	ctx context.Context,
	relative string,
) ([]byte, bool, error) {
	l.reads = append(l.reads, relative)
	return l.repairProbeLoader.ReadFileBytes(ctx, relative)
}

func TestRepairRefusesUnsupportedCitationPathsBeforeReading(t *testing.T) {
	t.Parallel()

	for _, relative := range []string{
		"semantic/notes with spaces.md",
		"semantic/topics/topic with spaces.md",
		"semantic/topics/caf\u00e9.md",
		"working/caf\u00e9.md",
		"working/trailing.md ",
	} {
		t.Run(relative, func(t *testing.T) {
			ctx := context.Background()
			content := strings.Repeat(
				"Preserve this exact file without reading or adopting it.\n",
				2048,
			)
			files := repairRawFiles(repairPreFiles())
			delete(files, repairNotesPath)
			files[relative] = []byte(content)
			loader := &repairPathProbeLoader{repairProbeLoader: &repairProbeLoader{
				semanticMemoryJobLoader: &semanticMemoryJobLoader{files: files},
				memoryFiles:             []MemoryFile{{Path: relative, Bytes: int64(len(content))}},
			}}
			ledgerBefore := string(files[workingLedgerRelativePath])
			model := &fakeModelClient{}
			job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			result, err := NewRunner(model, nil, []string{"test"}, loader).Run(ctx, job, nil)
			if err != nil || !result.NoWork {
				t.Fatalf("Run() = %#v, %v, want explicit no-work refusal", result, err)
			}
			if len(loader.reads) != 0 || len(loader.forbidden) != 0 || len(model.callMsgs) != 0 {
				t.Fatalf(
					"reads=%v typed loads=%v model calls=%d, want none",
					loader.reads,
					loader.forbidden,
					len(model.callMsgs),
				)
			}
			if len(loader.deleted) != 0 || string(files[relative]) != content ||
				string(files[workingLedgerRelativePath]) != ledgerBefore {
				t.Fatal(
					"unsupported path refusal changed content, deleted a file or appended an exit",
				)
			}
			refusals, err := loadRepairRefusals(ctx, loader)
			if err != nil || len(refusals) != 1 ||
				!strings.Contains(refusals[relative].Reason, "unsupported memory citation path") ||
				refusals[relative].RecordedAt.IsZero() {
				t.Fatalf(
					"durable refusals=%#v error=%v, want explicit unsupported-path reason",
					refusals,
					err,
				)
			}
			if run, _, err := evaluateMemorySurfaceRepairable(ctx, Snapshot{Loader: loader}, memoryFileBudgets{}); err != nil ||
				run {
				t.Fatalf("state rule after refusal: run=%v error=%v, want suppressed", run, err)
			}
			if len(loader.reads) != 0 {
				t.Fatalf("state rule read content %v", loader.reads)
			}

			// An operator renames the file into the citation grammar. The old
			// refusal is pruned and the same bytes become repairable normally.
			delete(files, relative)
			files[repairNotesPath] = []byte(content)
			loader.memoryFiles = []MemoryFile{{Path: repairNotesPath, Bytes: int64(len(content))}}
			if run, _, err := evaluateMemorySurfaceRepairable(ctx, Snapshot{Loader: loader}, memoryFileBudgets{}); err != nil ||
				!run {
				t.Fatalf("state rule after rename: run=%v error=%v, want repairable", run, err)
			}
			spec, err := job.Build(ctx, loader)
			if err != nil || !spec.repair || spec.repairTarget.Path != repairNotesPath {
				t.Fatalf(
					"Build after rename: target=%#v error=%v, want bounded repair",
					spec.repairTarget,
					err,
				)
			}
			if refusals, err := loadRepairRefusals(ctx, loader); err != nil || len(refusals) != 0 {
				t.Fatalf("refusals after rename=%#v error=%v, want old path pruned", refusals, err)
			}
		})
	}
}
