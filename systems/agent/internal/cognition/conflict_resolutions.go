package cognition

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

const insufficientConflictEvidence = "insufficient"

var conflictResolutionPattern = regexp.MustCompile(
	`^resolution claims=([^ ]+) winner=([^ ]+) reason=("(?:[^"\\]|\\.)*")(?: basis=([a-f0-9]{64}))?$`,
)

type conflictResolution struct {
	group  []conflictedClaimEntry
	ids    []string
	winner string
	reason string
	basis  string
}

func conflictGroupKey(group []conflictedClaimEntry) string {
	ids := conflictedClaimIDs(group)
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// The digest covers every parsed claim field and its path, including provenance
// and revision. A review of an older claim cannot resolve its replacement.
func conflictGroupBasis(group []conflictedClaimEntry) string {
	ordered := append([]conflictedClaimEntry(nil), group...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].relativePath != ordered[j].relativePath {
			return ordered[i].relativePath < ordered[j].relativePath
		}
		return ordered[i].claim.ID < ordered[j].claim.ID
	})
	var evidence strings.Builder
	for _, entry := range ordered {
		claim := entry.claim
		fmt.Fprintf(&evidence, "%q\n%q\n%q\n%q\n%q\n%q\n%q\n%q\n",
			entry.relativePath, claim.ID, claim.Text, claim.Status,
			claim.Kind, claim.Confidence, claim.Src, claim.Rev)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(evidence.String())))
}

func parseConflictResolution(line string) (conflictResolution, error) {
	match := conflictResolutionPattern.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return conflictResolution{}, fmt.Errorf("invalid conflict resolution record %q", line)
	}
	record := conflictResolution{
		ids: strings.Split(match[1], ","), winner: match[2], basis: match[4],
	}
	var err error
	record.reason, err = strconv.Unquote(match[3])
	if err != nil || strings.TrimSpace(record.reason) == "" ||
		strings.ContainsAny(record.reason, "\r\n") {
		return conflictResolution{}, fmt.Errorf(
			"conflict resolution requires a nonempty single-line reason",
		)
	}
	record.reason = strings.TrimSpace(record.reason)
	seen := make(map[string]bool, len(record.ids))
	for _, id := range record.ids {
		if !claimIDPattern.MatchString(id) || seen[id] {
			return conflictResolution{}, fmt.Errorf(
				"invalid or duplicate resolution claim ID %q",
				id,
			)
		}
		seen[id] = true
	}
	if record.winner != insufficientConflictEvidence && !seen[record.winner] {
		return conflictResolution{}, fmt.Errorf(
			"resolution winner %s is not a member of its conflict group",
			record.winner,
		)
	}
	sort.Strings(record.ids)
	return record, nil
}

func (r conflictResolution) key() string { return strings.Join(r.ids, ",") }

func (r conflictResolution) line() string {
	line := fmt.Sprintf("resolution claims=%s winner=%s reason=%q", r.key(), r.winner, r.reason)
	if r.basis != "" {
		line += " basis=" + r.basis
	}
	return line
}

func isConflictResolutionLine(line string) bool {
	fields := strings.Fields(line)
	return len(fields) > 0 && fields[0] == "resolution"
}

func bindConflictResolutions(content string, groups [][]conflictedClaimEntry) (string, error) {
	expected := make(map[string][]conflictedClaimEntry, len(groups))
	for _, group := range groups {
		expected[conflictGroupKey(group)] = group
	}
	seen := make(map[string]bool, len(groups))
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !isConflictResolutionLine(line) {
			continue
		}
		record, err := parseConflictResolution(line)
		if err != nil {
			return "", err
		}
		group, known := expected[record.key()]
		if !known || seen[record.key()] {
			return "", fmt.Errorf("unknown or duplicate conflict resolution for %s", record.key())
		}
		if record.basis != "" {
			return "", fmt.Errorf("conflict resolution basis is assigned by the framework")
		}
		seen[record.key()] = true
		record.basis = conflictGroupBasis(group)
		lines[i] = record.line()
	}
	if len(seen) != len(expected) {
		return "", fmt.Errorf(
			"verification review resolved %d of %d conflict groups",
			len(seen),
			len(expected),
		)
	}
	return strings.Join(lines, "\n"), nil
}

func conflictGroupsFromBaseline(baseline map[string]string) ([][]conflictedClaimEntry, error) {
	semantic := agent.SemanticMemory{}
	for runtimePath, content := range baseline {
		if semanticCapFor(runtimePath) > 0 {
			semantic.Files = append(semantic.Files, agent.SemanticMemoryFile{
				RelativePath: strings.TrimPrefix(
					runtimePath,
					memoryRuntimePrefix,
				), Content: content,
			})
		}
	}
	sort.Slice(semantic.Files, func(i, j int) bool {
		return semantic.Files[i].RelativePath < semantic.Files[j].RelativePath
	})
	conflicted, err := collectConflictedClaims(semantic, nil)
	if err != nil {
		return nil, err
	}
	return groupConflictedClaims(conflicted), nil
}

func currentConflictResolutions(
	artifact string,
	conflictedGroups [][]conflictedClaimEntry,
) ([]conflictResolution, error) {
	groups := make(map[string][]conflictedClaimEntry)
	for _, group := range conflictedGroups {
		groups[conflictGroupKey(group)] = group
	}
	var selected []conflictResolution
	seen := make(map[string]bool)
	for _, line := range strings.Split(artifact, "\n") {
		if !isConflictResolutionLine(line) {
			continue
		}
		record, err := parseConflictResolution(line)
		if err != nil {
			return nil, err
		}
		if seen[record.key()] {
			return nil, fmt.Errorf(
				"duplicate verification conflict resolution for %s",
				record.key(),
			)
		}
		seen[record.key()] = true
		group, exists := groups[record.key()]
		if !exists || record.basis != conflictGroupBasis(group) {
			continue
		}
		record.group = group
		selected = append(selected, record)
	}
	return selected, nil
}

func renderConflictResolutions(records []conflictResolution) string {
	lines := []string{
		"Apply every bound resolution below. A winner must remain active and every loser must be deleted; append exactly one resolved ledger record per loser, targeting its ID and using the note `winner=<winner-id>; <nonempty reason>`.",
		"An insufficient resolution requires every member claim, including its path, metadata, provenance and text, to remain unchanged and conflicted. Never use unbound or stale review prose to resolve a conflict.",
	}
	for _, record := range records {
		lines = append(lines, record.line(), renderConflictedClaimGroup(record.group))
	}
	return strings.Join(lines, "\n")
}

func checkConflictResolutions(ctx context.Context, loader ContextLoader, spec Spec) error {
	if len(spec.conflictGroups) == 0 {
		return nil
	}
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("list conflict resolution results: %w", err)
	}
	current := make(map[string]conflictedClaimEntry)
	for relativePath := range sizes {
		raw, present, err := loader.ReadFileBytes(ctx, relativePath)
		if err != nil {
			return fmt.Errorf("read conflict resolution result %s: %w", relativePath, err)
		}
		if !present {
			return fmt.Errorf("conflict resolution result %s is missing", relativePath)
		}
		parsed, _, err := claims.ParseClaims(string(raw))
		if err != nil {
			return err
		}
		for _, claim := range parsed {
			current[claim.ID] = conflictedClaimEntry{relativePath: relativePath, claim: claim}
		}
	}
	entries, err := appendedWorkingLedgerEntries(ctx, loader, spec.Baseline, "conflict resolution")
	if err != nil {
		return err
	}
	for _, record := range spec.conflictResolutions {
		if err := checkConflictResolution(record, current, entries); err != nil {
			return err
		}
	}
	for _, group := range spec.conflictGroups {
		reviewed := false
		for _, record := range spec.conflictResolutions {
			if record.key() == conflictGroupKey(group) {
				reviewed = true
				break
			}
		}
		if !reviewed {
			if err := checkConflictResolution(conflictResolution{
				ids: conflictedClaimIDs(group), winner: insufficientConflictEvidence,
				basis: conflictGroupBasis(group),
			}, current, entries); err != nil {
				return fmt.Errorf("unreviewed conflict group must remain unchanged: %w", err)
			}
		}
	}
	return nil
}

func checkConflictResolution(
	record conflictResolution,
	current map[string]conflictedClaimEntry,
	entries []claims.LedgerEntry,
) error {
	if record.winner == insufficientConflictEvidence {
		unchanged := make([]conflictedClaimEntry, 0, len(record.ids))
		for _, id := range record.ids {
			entry, exists := current[id]
			if !exists {
				return fmt.Errorf("insufficient conflict resolution removed claim %s", id)
			}
			unchanged = append(unchanged, entry)
		}
		if conflictGroupBasis(unchanged) != record.basis {
			return fmt.Errorf("insufficient conflict resolution changed claims %s", record.key())
		}
		for _, entry := range entries {
			for _, id := range record.ids {
				if entry.TargetID == id && (entry.Outcome == claims.OutcomeResolved ||
					entry.Outcome == claims.OutcomeSuperseded || entry.Outcome == claims.OutcomeRetracted) {
					return fmt.Errorf(
						"insufficient conflict resolution recorded a lifecycle exit for %s",
						id,
					)
				}
			}
		}
		return nil
	}
	winner, exists := current[record.winner]
	if !exists || winner.claim.EffectiveStatus() != claims.StatusActive {
		return fmt.Errorf("conflict resolution winner %s must remain active", record.winner)
	}
	for _, entry := range entries {
		if entry.TargetID == record.winner && (entry.Outcome == claims.OutcomeResolved ||
			entry.Outcome == claims.OutcomeSuperseded || entry.Outcome == claims.OutcomeRetracted) {
			return fmt.Errorf(
				"conflict resolution winner %s cannot have a lifecycle exit",
				record.winner,
			)
		}
	}
	for _, id := range record.ids {
		if id == record.winner {
			continue
		}
		if _, remains := current[id]; remains {
			return fmt.Errorf("conflict resolution loser %s remains in semantic memory", id)
		}
		matches := 0
		for _, entry := range entries {
			if entry.TargetID != id {
				continue
			}
			if entry.Outcome == claims.OutcomeSuperseded ||
				entry.Outcome == claims.OutcomeRetracted {
				return fmt.Errorf(
					"conflict resolution loser %s requires the resolved lifecycle outcome",
					id,
				)
			}
			if entry.Outcome == claims.OutcomeResolved {
				reason, matchesWinner := strings.CutPrefix(entry.Note, "winner="+record.winner+"; ")
				if !matchesWinner || strings.TrimSpace(reason) == "" {
					return fmt.Errorf(
						"conflict resolution ledger record for %s has the wrong winner or reason",
						id,
					)
				}
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf(
				"conflict resolution loser %s requires exactly one resolved ledger record with its winner and reason",
				id,
			)
		}
	}
	return nil
}
