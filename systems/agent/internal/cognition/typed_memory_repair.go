package cognition

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

type repairDecision struct {
	Offset  *uint64              `json:"offset"`
	Outcome claims.LedgerOutcome `json:"outcome"`
	ClaimID string               `json:"claim_id,omitempty"`
	Claim   *repairClaim         `json:"claim,omitempty"`
	Reason  string               `json:"reason,omitempty"`
	Loop    *repairLoop          `json:"loop,omitempty"`
}

type repairClaim struct {
	Path       string            `json:"path"`
	Text       string            `json:"text"`
	Status     claims.Status     `json:"status"`
	Kind       claims.Kind       `json:"kind"`
	Confidence claims.Confidence `json:"confidence"`
}

type repairLoop struct {
	ID      string `json:"id"`
	Trigger string `json:"trigger"`
	Owner   string `json:"owner"`
}

type repairWritePlan struct {
	ContextLoader
	files map[string][]byte
}

func (p repairWritePlan) ReadFileBytes(ctx context.Context, relative string) ([]byte, bool, error) {
	if raw, ok := p.files[relative]; ok {
		return append([]byte(nil), raw...), true, nil
	}
	return p.ContextLoader.ReadFileBytes(ctx, relative)
}

func (p repairWritePlan) LoadSemanticFileSizes(ctx context.Context) (map[string]int64, error) {
	sizes, err := p.ContextLoader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(sizes)+len(p.files))
	for relative, size := range sizes {
		result[relative] = size
	}
	for relative, raw := range p.files {
		if strings.HasPrefix(relative, "semantic/") {
			result[relative] = int64(len(raw))
		}
	}
	return result, nil
}

func applyRepairResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
	budgets memoryFileBudgets,
) (ParsedResult, error) {
	if loader == nil {
		return ParsedResult{}, fmt.Errorf("context loader is required")
	}
	plan, written, err := buildRepairWritePlan(ctx, loader, output)
	if err != nil {
		return ParsedResult{}, err
	}
	if _, err := validateRepairRun(ctx, plan, output, output.Spec.repairTarget, budgets, written); err != nil {
		return ParsedResult{}, err
	}
	for runtimePath, snapshot := range output.Spec.FileSnapshots {
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return ParsedResult{}, err
		}
		if existed != snapshot.Existed || !bytes.Equal(raw, snapshot.Content) {
			return ParsedResult{}, fmt.Errorf(
				"memory changed before applying repair plan: %s",
				runtimePath,
			)
		}
	}
	for _, runtimePath := range written {
		if _, existed := output.Spec.FileSnapshots[runtimePath]; existed {
			continue
		}
		_, exists, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return ParsedResult{}, err
		}
		if exists {
			return ParsedResult{}, fmt.Errorf(
				"repair destination appeared after evidence build: %s",
				runtimePath,
			)
		}
	}
	for _, runtimePath := range written {
		relative := relativeMemoryPath(runtimePath)
		if err := loader.WriteFileBytes(ctx, relative, plan.files[relative]); err != nil {
			return ParsedResult{}, rollbackFailedRun(
				ctx,
				loader,
				output.Spec.FileSnapshots,
				fmt.Errorf("apply repair plan %s: %w", relative, err),
			)
		}
	}
	return applyWrittenRepairResult(ctx, loader, output, budgets, written)
}

func buildRepairWritePlan(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (repairWritePlan, []string, error) {
	plan := repairWritePlan{ContextLoader: loader, files: make(map[string][]byte)}
	if err := ctx.Err(); err != nil {
		return plan, nil, err
	}
	var response struct {
		Decisions []repairDecision `json:"decisions"`
	}
	decoder := json.NewDecoder(strings.NewReader(output.FinalText))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return plan, nil, fmt.Errorf("decode repair decisions: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return plan, nil, fmt.Errorf("repair decisions must be one JSON object")
	}
	target := output.Spec.repairTarget
	runtimeTarget := memoryRuntimePrefix + target.Path
	snapshot, exists := output.Spec.FileSnapshots[runtimeTarget]
	if !exists || !snapshot.Existed {
		return plan, nil, fmt.Errorf("repair plan lacks target snapshot")
	}
	chunk, err := cutRepairChunk(snapshot.Content)
	if err != nil {
		return plan, nil, err
	}
	content := make(map[uint64]repairChunkLine)
	for _, line := range chunk {
		if repairContentLine(line.text) {
			content[uint64(line.offset)] = line
		}
	}
	// The model is asked for one decision per content line and nothing else,
	// and it does not always comply: it may decide a heading or a blank line
	// the runtime never drains, invent an offset, or repeat one while it
	// changes its mind. All three are harmless - a decision for a line this
	// chunk does not drain cannot be applied, and a repeat is the model's
	// later word about the same line - so they are resolved here instead of
	// refusing the run. Refusing them wedges the surface instead: a refusal
	// repeats as long as the model answers the same way, and a chunk that
	// cannot be applied makes no progress for the retry ladder to build on.
	// A content line left without any decision still refuses, and now refuses
	// by offset rather than by count.
	chosen := make(map[uint64]repairDecision, len(response.Decisions))
	for _, decision := range response.Decisions {
		if decision.Offset == nil {
			continue
		}
		if _, wanted := content[*decision.Offset]; !wanted {
			continue
		}
		chosen[*decision.Offset] = decision
	}
	decisions := make([]repairDecision, 0, len(content))
	for _, line := range chunk {
		offset := uint64(line.offset)
		if !repairContentLine(line.text) {
			continue
		}
		decision, present := chosen[offset]
		if !present {
			return plan, nil, fmt.Errorf(
				"repair decision offset %d is missing for %q",
				offset,
				normalizeExitDescription(line.text),
			)
		}
		decisions = append(decisions, decision)
	}
	seen := make(map[uint64]bool, len(content))
	for offset := range content {
		seen[offset] = true
	}
	definitions := make(map[string]repairClaim)
	claimSources := make(map[string][]string)
	var entries []claims.LedgerEntry
	at := output.Spec.RunAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	for _, decision := range decisions {
		offset := *decision.Offset
		line := content[offset]
		entry := claims.LedgerEntry{
			Timestamp:   at,
			Run:         semanticMemoryExtractionJobType,
			SourceSeq:   fmt.Sprintf("mem:%s:%d", target.Path, offset),
			Outcome:     decision.Outcome,
			Description: normalizeExitDescription(line.text),
		}
		switch decision.Outcome {
		case claims.OutcomeDropped:
			if decision.Claim != nil || decision.ClaimID != "" || decision.Loop != nil ||
				strings.TrimSpace(decision.Reason) == "" {
				return plan, nil, fmt.Errorf("dropped repair decision requires only a reason")
			}
			entry.Note = decision.Reason
		case claims.OutcomeConverted:
			if !claimIDPattern.MatchString(decision.ClaimID) || decision.Loop != nil ||
				decision.Reason != "" {
				return plan, nil, fmt.Errorf(
					"converted repair decision requires a claim ID and no loop or reason",
				)
			}
			entry.TargetID = decision.ClaimID
			claimSources[decision.ClaimID] = append(claimSources[decision.ClaimID], entry.SourceSeq)
			if decision.Claim != nil {
				if previous, exists := definitions[decision.ClaimID]; exists &&
					previous != *decision.Claim {
					return plan, nil, fmt.Errorf(
						"conflicting definitions of repair claim %s",
						decision.ClaimID,
					)
				}
				definitions[decision.ClaimID] = *decision.Claim
			}
		case claims.OutcomeMoved:
			if target.Path != workingMemoryRelativePath || decision.Loop == nil ||
				decision.Claim != nil ||
				decision.ClaimID != "" ||
				decision.Reason != "" {
				return plan, nil, fmt.Errorf(
					"moved repair decision requires a working-memory target and loop metadata",
				)
			}
			loop := decision.Loop
			intention := workingItemDescription(line.text)
			item := fmt.Sprintf(
				"- [%s] %s\n  {trigger: %s, owner: %s, src: %s}\n",
				loop.ID,
				intention,
				loop.Trigger,
				loop.Owner,
				entry.SourceSeq,
			)
			parsed, err := parseOpenLoops(item)
			if err != nil || len(parsed) != 1 || parsed[0].ID != loop.ID ||
				parsed[0].Text != intention ||
				parsed[0].Trigger != loop.Trigger ||
				parsed[0].Owner != loop.Owner {
				return plan, nil, fmt.Errorf("invalid repair loop metadata")
			}
			if _, ok := plan.files[openLoopsRelativePath]; !ok {
				plan.files[openLoopsRelativePath] = []byte(
					output.Spec.Baseline[openLoopsRuntimePath],
				)
			}
			plan.files[openLoopsRelativePath] = appendRepairRecord(
				plan.files[openLoopsRelativePath],
				item,
			)
			entry.TargetID, entry.TargetPath = loop.ID, openLoopsRelativePath
		default:
			return plan, nil, fmt.Errorf("unsupported repair outcome %q", decision.Outcome)
		}
		entries = append(entries, entry)
	}
	paths, err := plan.planClaims(output.Spec, definitions, claimSources, at)
	if err != nil {
		return plan, nil, err
	}
	for i := range entries {
		if entries[i].Outcome == claims.OutcomeConverted {
			entries[i].TargetPath = paths[entries[i].TargetID]
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		_, a, _ := claims.ParseMemorySource(entries[i].SourceSeq)
		_, b, _ := claims.ParseMemorySource(entries[j].SourceSeq)
		return a < b
	})
	ledger := []byte(output.Spec.Baseline[workingLedgerRuntimePath])
	for _, entry := range entries {
		line := claims.FormatLedgerEntry(entry)
		parsed, err := claims.ParseLedgerEntry(strings.TrimSuffix(line, "\n"))
		if err != nil || parsed.Description != entry.Description ||
			parsed.SourceSeq != entry.SourceSeq ||
			parsed.Outcome != entry.Outcome ||
			parsed.TargetID != entry.TargetID ||
			parsed.TargetPath != entry.TargetPath ||
			normalizeExitDescription(parsed.Note) != normalizeExitDescription(entry.Note) {
			return plan, nil, fmt.Errorf(
				"repair decision cannot be represented by the ledger grammar",
			)
		}
		ledger = appendRepairRecord(ledger, line)
	}
	plan.files[workingLedgerRelativePath] = ledger
	plan.files[target.Path] = retainedRepairBytes(snapshot.Content, chunk, seen, target.Path)
	var written []string
	for relative := range plan.files {
		if relative != target.Path && relative != workingLedgerRelativePath {
			written = append(written, memoryRuntimePrefix+relative)
		}
	}
	sort.Strings(written)
	written = append(written, workingLedgerRuntimePath, runtimeTarget)
	return plan, written, nil
}

func appendRepairRecord(raw []byte, record string) []byte {
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	return append(raw, []byte(record)...)
}

func retainedRepairBytes(
	pre []byte,
	chunk []repairChunkLine,
	drained map[uint64]bool,
	relative string,
) []byte {
	var post []byte
	cursor := 0
	keptHeadings := make(map[string]bool)
	for _, line := range chunk {
		remove := drained[uint64(line.offset)]
		if len(drained) == 0 && !repairContentLine(line.text) {
			heading := strings.TrimSpace(line.text)
			protected := checkRepairHeadings(relative, []byte(line.text), nil) != nil ||
				(line.offset == 0 && strings.HasPrefix(heading, "# "))
			remove = !protected || keptHeadings[heading]
			if protected {
				keptHeadings[heading] = true
			}
		}
		if !remove {
			continue
		}
		end := line.offset + len(line.text)
		if end < len(pre) && pre[end] == '\n' {
			end++
		}
		post = append(post, pre[cursor:line.offset]...)
		cursor = end
	}
	return append(post, pre[cursor:]...)
}

func (p repairWritePlan) planClaims(
	spec Spec,
	definitions map[string]repairClaim,
	sources map[string][]string,
	at time.Time,
) (map[string]string, error) {
	type located struct {
		claim claims.Claim
		path  string
	}
	previous := make(map[string]located)
	for runtimePath, raw := range spec.Baseline {
		if !semanticClaimPath(runtimePath) {
			continue
		}
		parsed, _, err := claims.ParseClaims(raw)
		if err != nil {
			return nil, err
		}
		for _, claim := range parsed {
			if _, duplicate := previous[claim.ID]; duplicate {
				return nil, fmt.Errorf("duplicate existing claim %s", claim.ID)
			}
			previous[claim.ID] = located{claim, relativeMemoryPath(runtimePath)}
		}
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	paths := make(map[string]string)
	for _, id := range ids {
		old, exists := previous[id]
		claim := old.claim
		claim.ID = id
		destination := old.path
		if definition, defined := definitions[id]; defined {
			destination = definition.Path
			claim.Text, claim.Status, claim.Kind, claim.Confidence = definition.Text, definition.Status, definition.Kind, definition.Confidence
		} else if !exists {
			return nil, fmt.Errorf("new repair claim %s needs a definition", id)
		}
		if path.Clean(destination) != destination ||
			!claims.ValidMemorySource("mem:"+destination+":0") ||
			!semanticClaimPath(memoryRuntimePrefix+destination) ||
			destination == spec.repairTarget.Path {
			return nil, fmt.Errorf("invalid repair claim destination %q", destination)
		}
		for _, source := range sources[id] {
			if !accessIncludesPath(claim.Src, source) {
				claim.Src = append(claim.Src, source)
			}
		}
		claim.Rev = at.UTC().Format(time.DateOnly)
		rendered := claims.RenderClaim(claim)
		if err := validateClaimItem(rendered); err != nil {
			return nil, err
		}
		parsed, _, err := claims.ParseClaims(rendered)
		if err != nil || len(parsed) != 1 || parsed[0].Text != claim.Text ||
			parsed[0].EffectiveStatus() != claim.EffectiveStatus() || parsed[0].Kind != claim.Kind || parsed[0].Confidence != claim.Confidence || !slices.Equal(parsed[0].Src, claim.Src) {
			return nil, fmt.Errorf("repair claim %s does not round-trip", id)
		}
		load := func(relative string) string {
			if raw, ok := p.files[relative]; ok {
				return string(raw)
			}
			return spec.Baseline[memoryRuntimePrefix+relative]
		}
		if exists && old.path != spec.repairTarget.Path {
			replacement := ""
			if old.path == destination {
				replacement = rendered
			}
			raw, err := claims.ReplaceClaim(load(old.path), id, replacement)
			if err != nil {
				return nil, err
			}
			p.files[old.path] = []byte(raw)
		}
		if !exists || old.path != destination {
			raw := load(destination)
			if raw == "" {
				raw = "# Claims\n\n"
			}
			p.files[destination] = appendRepairRecord([]byte(raw), rendered)
		}
		paths[id] = destination
	}
	return paths, nil
}
