package memory

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
)

// The status report in this file is a read-only observation of a memory root:
// where the transcript head stands, how far behind each conversion checkpoint
// is, what the cognition jobs have been failing, and how close every sized
// file is to the limit it is judged against. Everything is read through the
// store's existing load methods, so a report never writes, commits or calls
// Store.Init; the store's committer is not invoked on any read path.
//
// The report code lives beside the store rather than beside the CLI so a test
// can drive it against a fixture root, and the verb in internal/cli stays a
// thin flag layer over CollectStatus and RenderStatus.

// StatusLimits are the byte budgets file sizes are judged against. They come
// from the caller so the renderer and the collector keep no dependency on the
// config package; the CLI passes the configuration defaults.
type StatusLimits struct {
	// WorkingMemoryBudgetBytes is the budget working memory is judged against,
	// the one whose overrun fires the conversion pass.
	WorkingMemoryBudgetBytes int64
	// OpenLoopsBudgetBytes is the budget open loops are judged against.
	OpenLoopsBudgetBytes int64
}

// Status is one observation of a memory root. Timestamps are the raw instants
// the store loaded; age is measured against the now CollectStatus was given,
// so a report is reproducible in tests.
type Status struct {
	// HeadSeq and HeadUpdatedAt are the transcript head and when head state
	// was last written.
	HeadSeq       int64
	HeadUpdatedAt time.Time

	// ConsolidationCheckpoint and SemanticExtractionCheckpoint are the two
	// conversion checkpoints relative to the head.
	ConsolidationCheckpoint      CheckpointStatus
	SemanticExtractionCheckpoint CheckpointStatus

	// Jobs are the built-in cognition jobs, in conversion order, with their
	// persisted failure state.
	Jobs []JobStatus

	// Files are working memory, open loops and every semantic file, in that
	// order, semantic files sorted by path.
	Files []FileSizeStatus

	// LedgerLines is what claims.LedgerEntryCount counts: non-empty lines of
	// the current ledger, which includes its format header.
	LedgerLines int
}

// CheckpointStatus is where one conversion checkpoint stands relative to the
// head. Lag is head seq minus checkpoint seq. Timestamp is when the checkpoint
// last advanced, and Age is measured by CollectStatus's now, so a zero
// Timestamp is the only signal a checkpoint has never advanced.
type CheckpointStatus struct {
	Seq       int64
	Timestamp time.Time
	Lag       int64
	Age       time.Duration
}

// JobStatus is the persisted failure state of one cognition job.
type JobStatus struct {
	Type                string
	ConsecutiveFailures int
	LastRunAt           time.Time
	LastSuccessAt       time.Time
	LastFailureAt       time.Time
}

// FileSizeKind says whether a file's limit is a budget or a cap, so an over
// mark can say which one was exceeded.
type FileSizeKind string

const (
	// FileSizeBudget is a budget whose overrun triggers a run.
	FileSizeBudget FileSizeKind = "budget"
	// FileSizeCap is a post-run cap on a semantic file.
	FileSizeCap FileSizeKind = "cap"
)

// FileSizeStatus is one file's size and the limit it is judged against.
type FileSizeStatus struct {
	Path       string
	Bytes      int64
	LimitBytes int64
	Kind       FileSizeKind
	Over       bool
}

// statusJobTypes are the job types status reports on, in conversion order:
// working memory is consolidated from the transcript, extraction converts it
// into semantic memory, and verification reviews the result.
var statusJobTypes = []string{
	cognition.WorkingMemoryConsolidationJobType,
	cognition.SemanticMemoryExtractionJobType,
	cognition.VerificationReviewJobType,
}

// CollectStatus reads a memory root through the store and reports what it
// found. It is read-only: it calls no store method that writes, and now drives
// every age. A root that has no head state is an error rather than an empty
// report, because a path that is not a memory root is a mistake worth naming.
func CollectStatus(
	ctx context.Context,
	s *Store,
	limits StatusLimits,
	now time.Time,
) (Status, error) {
	if limits.WorkingMemoryBudgetBytes <= 0 || limits.OpenLoopsBudgetBytes <= 0 {
		return Status{}, fmt.Errorf(
			"memory status limits must be positive: working %d, open loops %d",
			limits.WorkingMemoryBudgetBytes,
			limits.OpenLoopsBudgetBytes,
		)
	}

	headSeq, headUpdatedAt, err := s.LoadHead(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load head: %w", err)
	}
	consolidation, err := s.LoadConsolidationCheckpoint(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load consolidation checkpoint: %w", err)
	}
	semantic, err := s.LoadSemanticExtractionCheckpoint(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load semantic extraction checkpoint: %w", err)
	}

	status := Status{
		HeadSeq:       headSeq,
		HeadUpdatedAt: headUpdatedAt,
		ConsolidationCheckpoint: checkpointStatus(
			consolidation.LastConsolidatedSeq,
			consolidation.UpdatedAt,
			headSeq,
			now,
		),
		SemanticExtractionCheckpoint: checkpointStatus(
			semantic.LastExtractedSeq,
			semantic.UpdatedAt,
			headSeq,
			now,
		),
	}

	for _, jobType := range statusJobTypes {
		state, err := s.LoadJobState(ctx, jobType)
		if err != nil {
			return Status{}, fmt.Errorf("load job state %s: %w", jobType, err)
		}
		status.Jobs = append(status.Jobs, JobStatus{
			Type:                jobType,
			ConsecutiveFailures: state.ConsecutiveFailures,
			LastRunAt:           state.LastRunAt,
			LastSuccessAt:       state.LastSuccessAt,
			LastFailureAt:       state.LastFailureAt,
		})
	}

	working, err := s.LoadWorkingMemory(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load working memory: %w", err)
	}
	openLoops, err := s.LoadOpenLoops(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load open loops: %w", err)
	}

	// Working memory and open loops are measured the way the runtime rules
	// measure them, as the length of the loaded content, so a report and the
	// trigger it mirrors cannot disagree by a trailing newline.
	status.Files = append(
		status.Files,
		fileSizeStatus(
			filepath.ToSlash(filepath.Join(workingDirPath, workingMemoryFileName)),
			int64(len(working.Content)),
			limits.WorkingMemoryBudgetBytes,
			FileSizeBudget,
		),
		fileSizeStatus(
			filepath.ToSlash(filepath.Join(workingDirPath, openLoopsFileName)),
			int64(len(openLoops)),
			limits.OpenLoopsBudgetBytes,
			FileSizeBudget,
		),
	)

	sizes, err := s.LoadSemanticFileSizes(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load semantic file sizes: %w", err)
	}
	semanticPaths := make([]string, 0, len(sizes))
	for relative := range sizes {
		semanticPaths = append(semanticPaths, relative)
	}
	sort.Strings(semanticPaths)
	for _, relative := range semanticPaths {
		limit := int64(cognition.SemanticCapBytes(relative))
		if limit <= 0 {
			// LoadSemanticFileSizes only lists the three roots and direct
			// topics children, all of which carry a cap. If a location without
			// one ever joins the list, refusing to report is the honest move:
			// a size printed without its limit would hide exactly the bloat
			// this report exists to surface.
			return Status{}, fmt.Errorf("semantic file %q has no cap", relative)
		}
		status.Files = append(
			status.Files,
			fileSizeStatus(relative, sizes[relative], limit, FileSizeCap),
		)
	}

	ledgerPath := filepath.Join(s.root(), filepath.FromSlash(workingLedgerRelativePath))
	lines, err := claims.LedgerEntryCount(ledgerPath)
	if err != nil {
		return Status{}, fmt.Errorf("count ledger lines: %w", err)
	}
	status.LedgerLines = lines

	return status, nil
}

// checkpointStatus builds one checkpoint's report line values. A checkpoint
// that never advanced has a zero timestamp; its age is left zero and the
// renderer says so rather than reporting an age since the zero time.
func checkpointStatus(seq int64, updated time.Time, headSeq int64, now time.Time) CheckpointStatus {
	status := CheckpointStatus{Seq: seq, Timestamp: updated, Lag: headSeq - seq}
	if !updated.IsZero() {
		status.Age = now.Sub(updated)
	}
	return status
}

// fileSizeStatus derives the over mark with the shared size-against-limit
// comparison, which is strict like the runtime rules: exactly at the limit is
// within it.
func fileSizeStatus(path string, bytes, limit int64, kind FileSizeKind) FileSizeStatus {
	return FileSizeStatus{
		Path:       path,
		Bytes:      bytes,
		LimitBytes: limit,
		Kind:       kind,
		Over:       memcheck.OverLimit(bytes, limit),
	}
}

// RenderStatus writes the report as stable line-oriented text: one subject per
// line, one word for each value, timestamps in UTC, and a mark at the end of
// every line whose subject is over its limit. The same state always renders
// the same bytes, so two runs diff like logs. The report is built whole and
// written in one call, so a failing writer is one error rather than a
// half-written report.
func RenderStatus(w io.Writer, status Status) error {
	_, err := io.WriteString(w, renderStatus(status))
	return err
}

func renderStatus(status Status) string {
	var out strings.Builder
	fmt.Fprintf(&out, "head seq %d at %s\n", status.HeadSeq, formatStatusTime(status.HeadUpdatedAt))
	writeCheckpointStatus(&out, "consolidation", status.ConsolidationCheckpoint)
	writeCheckpointStatus(&out, "semantic-extraction", status.SemanticExtractionCheckpoint)
	for _, job := range status.Jobs {
		fmt.Fprintf(
			&out,
			"job %s failures %d last-run %s last-success %s last-failure %s\n",
			job.Type,
			job.ConsecutiveFailures,
			formatStatusTime(job.LastRunAt),
			formatStatusTime(job.LastSuccessAt),
			formatStatusTime(job.LastFailureAt),
		)
	}
	for _, file := range status.Files {
		fmt.Fprintf(
			&out,
			"file %s %d of %d bytes%s\n",
			file.Path,
			file.Bytes,
			file.LimitBytes,
			fileSizeMark(file),
		)
	}
	fmt.Fprintf(&out, "ledger lines %d\n", status.LedgerLines)
	return out.String()
}

func writeCheckpointStatus(w io.Writer, name string, checkpoint CheckpointStatus) {
	span := "age never at never"
	if !checkpoint.Timestamp.IsZero() {
		span = fmt.Sprintf("age %s at %s", checkpoint.Age, formatStatusTime(checkpoint.Timestamp))
	}
	fmt.Fprintf(w, "checkpoint %s seq %d lag %d %s\n", name, checkpoint.Seq, checkpoint.Lag, span)
}

func fileSizeMark(file FileSizeStatus) string {
	if !file.Over {
		return ""
	}
	switch file.Kind {
	case FileSizeBudget:
		return " OVER BUDGET"
	case FileSizeCap:
		return " OVER CAP"
	}
	return ""
}

// formatStatusTime renders a loaded instant in UTC, or the word an operator
// reads when the fact behind it has never happened.
func formatStatusTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}
