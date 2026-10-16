package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// repairRefusal is one durable refusal: a named memory-surface target whose
// first content line is longer than repairChunkMaxBytes, so no repair run
// can ever drain it. A named file is schema-owned and is never deleted, so
// the refusal is the record that stands in for the adoption an un-named
// file would get: it names the path, the reason, and when it was written.
//
// An entry is cleared three ways, and only these:
//   - applyRepairResult drops it when a repair run succeeds on the path
//     (repaired).
//   - repairSpec prunes it when the surface scan no longer lists the path as
//     repairable (edited back inside the schema) or at all (gone).
//   - Nothing else; while the path stays a refused repairable target the
//     entry must stay, or the state rule would fire on it again.
type repairRefusal struct {
	Path       string    `json:"path"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
}

// repairRefusalsFile is the on-disk shape of the refusal set.
type repairRefusalsFile struct {
	Refusals []repairRefusal `json:"refusals"`
}

// loadRepairRefusals reads the durable refusal set the repair pass owns,
// keyed by surface path. An absent, empty or undecodable artifact is an empty
// set: the refusals are a suppression list, not memory, and every surface rule
// evaluation loads them, so failing here would let one bad byte stop the whole
// runtime.
func loadRepairRefusals(
	ctx context.Context,
	loader ContextLoader,
) (map[string]repairRefusal, error) {
	artifact, err := loader.LoadCognitionArtifact(ctx, repairRefusalsArtifactPath)
	if err != nil {
		return nil, fmt.Errorf("load repair refusals: %w", err)
	}
	set := make(map[string]repairRefusal)
	content := strings.TrimSpace(artifact.Content)
	if content == "" {
		return set, nil
	}
	var file repairRefusalsFile
	if err := json.Unmarshal([]byte(content), &file); err != nil {
		// Deliberately not an error. This artifact is job-owned and rewritten
		// by the next recording or prune, so the worst case of ignoring a
		// damaged one is a re-refusal; propagating it would instead fail every
		// rule evaluation, fail the build, and cancel the runtime in a boot
		// loop that no run can break.
		return set, nil
	}
	for _, refusal := range file.Refusals {
		if refusal.Path == "" {
			continue
		}
		set[refusal.Path] = refusal
	}
	return set, nil
}

// storeRepairRefusals persists the refusal set through the job-owned
// cognition artifact, ordered by path so the file is stable. An empty set is
// stored as an empty list rather than deleted: the loader side of the job
// speaks only Load/StoreCognitionArtifact, and absent and empty both read as
// no refusals.
func storeRepairRefusals(
	ctx context.Context,
	loader ContextLoader,
	set map[string]repairRefusal,
) error {
	ordered := make([]repairRefusal, 0, len(set))
	for _, refusal := range set {
		ordered = append(ordered, refusal)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	raw, err := json.MarshalIndent(repairRefusalsFile{Refusals: ordered}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal repair refusals: %w", err)
	}
	if err := loader.StoreCognitionArtifact(ctx, Artifact{
		RelativePath: repairRefusalsArtifactPath,
		Content:      string(raw) + "\n",
	}); err != nil {
		return fmt.Errorf("store repair refusals: %w", err)
	}
	return nil
}

// withoutRefusedRepairTargets applies the refusal set the way every surface
// caller must - the scan itself stays stat-only: refused paths drop out of
// the target list, so the state rule stops firing on them and the repair
// spec moves to the next repairable target.
func withoutRefusedRepairTargets(
	targets []MemorySurfaceTarget,
	refusals map[string]repairRefusal,
) []MemorySurfaceTarget {
	if len(refusals) == 0 {
		return targets
	}
	kept := make([]MemorySurfaceTarget, 0, len(targets))
	for _, target := range targets {
		if _, refused := refusals[target.Path]; !refused {
			kept = append(kept, target)
		}
	}
	return kept
}

// pruneRepairRefusals drops every refusal whose path the current surface no
// longer lists as repairable - the path was repaired, edited back inside the
// schema, or is gone - reporting whether anything changed so the caller can
// persist the pruning.
func pruneRepairRefusals(
	set map[string]repairRefusal,
	surface MemorySurface,
) (map[string]repairRefusal, bool) {
	repairable := make(map[string]bool, len(surface.Targets))
	for _, target := range surface.Targets {
		repairable[target.Path] = true
	}
	changed := false
	for path := range set {
		if !repairable[path] {
			delete(set, path)
			changed = true
		}
	}
	return set, changed
}

// recordRepairRefusal adds a durable refusal for one named target whose
// first line no repair window can hold, and persists the set. The runtime
// records it without a model run: draining the line would need a window that
// by construction cannot hold it.
func recordRepairRefusal(
	ctx context.Context,
	loader ContextLoader,
	set map[string]repairRefusal,
	target MemorySurfaceTarget,
	tooLong *repairLineTooLongError,
) error {
	set[target.Path] = repairRefusal{
		Path: target.Path,
		Reason: fmt.Sprintf(
			"first content line is %d bytes, over the %d-byte maximum repair window, so no repair run can drain it",
			tooLong.lineBytes,
			repairChunkMaxBytes,
		),
		RecordedAt: time.Now().UTC(),
	}
	if err := storeRepairRefusals(ctx, loader, set); err != nil {
		return fmt.Errorf("record repair refusal for %s: %w", target.Path, err)
	}
	return nil
}

// clearRepairRefusal drops the refusal for one path after its successful
// repair run. The common path has nothing to clear and writes nothing.
func clearRepairRefusal(ctx context.Context, loader ContextLoader, path string) error {
	set, err := loadRepairRefusals(ctx, loader)
	if err != nil {
		return err
	}
	if _, refused := set[path]; !refused {
		return nil
	}
	delete(set, path)
	if err := storeRepairRefusals(ctx, loader, set); err != nil {
		return fmt.Errorf("clear repair refusal for %s: %w", path, err)
	}
	return nil
}
