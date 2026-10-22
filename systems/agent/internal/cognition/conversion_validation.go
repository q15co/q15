package cognition

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

// claimSrcRefusalError is the typed src-grammar refusal: a claim's src field,
// as documented in docs/architecture.md under "Semantic claim src citations",
// did not parse as citation evidence. The conversion apply boundary defers
// exactly this refusal for never-before-seen claims (issue #390); every other
// refusal still fails the whole run.
type claimSrcRefusalError struct{ src string }

func (e claimSrcRefusalError) Error() string {
	return fmt.Sprintf("invalid claim src %q", e.src)
}

var (
	claimIDPattern           = regexp.MustCompile(`^c-[0-9]{4}-[0-9]{4,}$`)
	loopHeadPattern          = regexp.MustCompile(`^[-*+] \[(l-[0-9]{4}-[0-9]{4,})\]\s+(.+)`)
	sourceIDPattern          = regexp.MustCompile(`^t[1-9][0-9]*$`)
	historicalClaimIDPattern = regexp.MustCompile(`\bc-[0-9]{4}-[0-9]{4,}\b`)
	// metadataKeyPattern splits a metadata span into fields. Only keys the
	// claim and loop grammars know count as field starts, so a value such as
	// a mem: memory citation inside a src list is never read as a field of
	// its own.
	// The key set is the one the claims grammar defines (see
	// claims.metadataFieldPattern) plus the two open-loop keys, so a value such
	// as a mem: memory citation inside a src list is never read as a field of
	// its own. An unknown key now fails the span as malformed rather than
	// being named, which is still a refusal.
	metadataKeyPattern = regexp.MustCompile(
		`(?:^|,\s*)(status|kind|confidence|src|rev|id|trigger|owner):\s*`,
	)
	metadataSpanPattern = regexp.MustCompile(`\{((?:[^{}"]|"(?:\\.|[^"\\])*")*)\}\s*$`)
	topicIndexPattern   = regexp.MustCompile(
		`^[-*+] \[([^\]\s/\\]+)\] ([^\r\n]+) → topics/([^\s/\\]+)\.md$`,
	)
)

// conversionBaseline captures raw evidence rather than prompt-normalized text.
func conversionBaselineFiles(ctx context.Context, loader ContextLoader) (map[string]string, error) {
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list conversion baseline files: %w", err)
	}
	paths := append(
		semanticRuntimePaths(),
		workingMemoryRuntimePath,
		workingLedgerRuntimePath,
		openLoopsRuntimePath,
	)
	for relative := range sizes {
		paths = append(paths, memoryRuntimePrefix+relative)
	}
	baseline := make(map[string]string, len(paths))
	for _, runtimePath := range paths {
		raw, _, err := loader.ReadFileBytes(
			ctx,
			strings.TrimPrefix(runtimePath, memoryRuntimePrefix),
		)
		if err != nil {
			return nil, fmt.Errorf("read conversion baseline %s: %w", runtimePath, err)
		}
		baseline[runtimePath] = string(raw)
	}
	return baseline, nil
}

func validateExitTargets(
	ctx context.Context,
	loader ContextLoader,
	entries []claims.LedgerEntry,
	deferred map[string]claimSrcDeferral,
) error {
	for _, entry := range entries {
		switch entry.Outcome {
		case claims.OutcomeDropped:
			if entry.TargetID != "" || entry.TargetPath != "" ||
				strings.TrimSpace(entry.Note) == "" {
				return fmt.Errorf("dropped ledger exit requires a reason and no target")
			}
		case claims.OutcomeConverted:
			runtimePath := memoryRuntimePrefix + entry.TargetPath
			if !semanticClaimPath(runtimePath) || !claimIDPattern.MatchString(entry.TargetID) {
				return fmt.Errorf(
					"converted ledger exit has invalid claim target %q (%s)",
					entry.TargetID,
					entry.TargetPath,
				)
			}
			raw, existed, err := loader.ReadFileBytes(ctx, entry.TargetPath)
			if err != nil {
				return fmt.Errorf("read converted ledger target: %w", err)
			}
			if !existed {
				return fmt.Errorf("converted ledger target %s is missing", entry.TargetPath)
			}
			found := false
			for _, item := range workingItems(string(raw)) {
				parsed, _, err := claims.ParseClaims(item)
				if err != nil {
					return err
				}
				if len(parsed) != 1 || parsed[0].ID != entry.TargetID {
					continue
				}
				if err := validateClaimItem(item); err != nil {
					return fmt.Errorf("invalid converted target %s: %w", entry.TargetID, err)
				}
				found = true
			}
			if !found {
				if deferral, ok := deferred[entry.TargetID]; ok &&
					deferral.Path == entry.TargetPath {
					// The apply step deferred this claim's src citations and
					// removed it from the written file. The exit still records
					// the completed working-memory exit it names, and the run
					// result discloses the deferral.
					continue
				}
				return fmt.Errorf(
					"converted ledger target %s does not exist in %s",
					entry.TargetID,
					entry.TargetPath,
				)
			}
		case claims.OutcomeMoved:
			if entry.TargetPath != "working/OPEN_LOOPS.md" {
				return fmt.Errorf("moved ledger exit must target working/OPEN_LOOPS.md")
			}
			raw, _, err := loader.ReadFileBytes(ctx, entry.TargetPath)
			if err != nil {
				return fmt.Errorf("read moved ledger target: %w", err)
			}
			loops, err := parseOpenLoops(string(raw))
			if err != nil {
				return fmt.Errorf("invalid moved target %s: %w", entry.TargetID, err)
			}
			found := false
			for _, loop := range loops {
				if loop.ID == entry.TargetID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("moved ledger target %s does not exist", entry.TargetID)
			}
		}
	}
	return nil
}

// Inventories also cover non-Markdown topics for caps and rollback. Durable
// claims must live in files the semantic topic loader and embed scanner read.
func semanticClaimPath(runtimePath string) bool {
	return semanticCapFor(runtimePath) > 0 && strings.EqualFold(path.Ext(runtimePath), ".md")
}

func validateClaimItem(item string) error {
	parsed, _, err := claims.ParseClaims(item)
	if err != nil {
		return err
	}
	if len(parsed) != 1 || !claimIDPattern.MatchString(parsed[0].ID) ||
		strings.TrimSpace(parsed[0].Text) == "" {
		return fmt.Errorf("claim requires a stable ID and statement")
	}
	fields, err := itemMetadata(item, "status", "kind", "confidence", "src", "rev")
	if err != nil {
		return err
	}
	if fields["status"] != "active" && fields["status"] != "conflicted" {
		return fmt.Errorf("invalid claim status %q", fields["status"])
	}
	switch fields["kind"] {
	case "fact", "inference", "preference":
	default:
		return fmt.Errorf("invalid claim kind %q", fields["kind"])
	}
	switch fields["confidence"] {
	case "confirmed", "likely", "uncertain":
	default:
		return fmt.Errorf("invalid claim confidence %q", fields["confidence"])
	}
	// The rev check comes before src so the typed src refusal proves the
	// claim's other fields already validated; the apply boundary reads that
	// guarantee when it decides to defer a claim instead of failing a run.
	if _, err := time.Parse(time.DateOnly, fields["rev"]); err != nil {
		return fmt.Errorf("invalid claim rev %q", fields["rev"])
	}
	if !validSourceList(fields["src"]) {
		return claimSrcRefusalError{src: fields["src"]}
	}
	return nil
}

// claimSrcDeferral records one claim the conversion apply step removed from
// the written file instead of failing the run (issue #390): a
// never-before-seen claim whose src citations alone failed the grammar. The
// deferral is disclosed in the run result so the pass's valid claims still
// apply and the extraction checkpoint still advances.
type claimSrcDeferral struct {
	// ID is the deferred claim's stable identifier.
	ID string
	// Path is the repository-relative semantic file the claim was removed
	// from, e.g. "semantic/facts.md".
	Path string
	// Reason is the bounded src-grammar refusal text.
	Reason string
}

const (
	// semanticDeferredClaimListLimit bounds how many deferred claims a run
	// result names individually; the overflow is disclosed as a count.
	semanticDeferredClaimListLimit = 8
	// semanticDeferredSrcTokenBytes bounds one disclosed citation value, so a
	// garbage src cannot bloat the run record.
	semanticDeferredSrcTokenBytes = 200
)

// deferSrcInvalidClaim removes one src-invalid claim item from content and
// returns its deferral record. It defers only a refusal of the typed
// src-grammar error on a newly introduced claim - a claim the baseline
// semantic files know is a rewrite of durable knowledge and fails the run
// instead - and only when the item's removal actually succeeds; anything
// else keeps today's whole-pass failure.
func deferSrcInvalidClaim(
	content, item string,
	validationErr error,
	before map[string]claims.Claim,
) (string, claimSrcDeferral, bool) {
	var refusal claimSrcRefusalError
	if !errors.As(validationErr, &refusal) {
		return content, claimSrcDeferral{}, false
	}
	parsed, _, err := claims.ParseClaims(item)
	if err != nil || len(parsed) != 1 || !claimIDPattern.MatchString(parsed[0].ID) {
		return content, claimSrcDeferral{}, false
	}
	if _, existed := before[parsed[0].ID]; existed {
		return content, claimSrcDeferral{}, false
	}
	cleaned, err := claims.ReplaceClaim(content, parsed[0].ID, "")
	if err != nil {
		return content, claimSrcDeferral{}, false
	}
	return cleaned, claimSrcDeferral{
		ID: parsed[0].ID,
		Reason: claimSrcRefusalError{
			src: boundedDeferredSrc(refusal.src),
		}.Error(),
	}, true
}

// boundedDeferredSrc collapses whitespace in a deferred citation value and
// caps its length, so the refusal text the run result names stays bounded.
func boundedDeferredSrc(src string) string {
	src = compactSemanticMemoryText(src)
	runes := []rune(src)
	if len(runes) <= semanticDeferredSrcTokenBytes {
		return src
	}
	return string(runes[:semanticDeferredSrcTokenBytes-3]) + "..."
}

func validateLoopItem(item string) error {
	match := loopHeadPattern.FindStringSubmatch(item)
	if match == nil || strings.TrimSpace(strings.SplitN(match[2], "{", 2)[0]) == "" {
		return fmt.Errorf("loop requires a stable ID and intention")
	}
	fields, err := itemMetadata(item, "trigger", "owner", "src")
	if err != nil {
		return err
	}
	if fields["owner"] != "agent" && fields["owner"] != "user" {
		return fmt.Errorf("invalid loop owner %q", fields["owner"])
	}
	if !sourceIDPattern.MatchString(fields["src"]) && !claims.ValidMemorySource(fields["src"]) {
		return fmt.Errorf("invalid loop src %q", fields["src"])
	}
	trigger := fields["trigger"]
	if trigger == "next-session" {
		return nil
	}
	if date, ok := strings.CutPrefix(trigger, "date:"); ok {
		if _, err := time.Parse(time.DateOnly, date); err == nil {
			return nil
		}
	}
	if quoted, ok := strings.CutPrefix(trigger, "event:"); ok {
		if event, err := strconv.Unquote(quoted); err == nil && strings.TrimSpace(event) != "" {
			return nil
		}
	}
	return fmt.Errorf("invalid loop trigger %q", trigger)
}

func itemMetadata(item string, required ...string) (map[string]string, error) {
	span := metadataSpanPattern.FindStringSubmatch(item)
	if span == nil {
		return nil, fmt.Errorf("missing canonical metadata")
	}
	raw := strings.TrimSpace(span[1])
	matches := metadataKeyPattern.FindAllStringSubmatchIndex(raw, -1)
	outsideQuotes := make([]bool, len(raw))
	quoted, escaped := false, false
	for i, char := range []byte(raw) {
		outsideQuotes[i] = !quoted
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && quoted {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
		}
	}
	filtered := matches[:0]
	for _, match := range matches {
		if outsideQuotes[match[0]] {
			filtered = append(filtered, match)
		}
	}
	matches = filtered
	if len(matches) == 0 || matches[0][0] != 0 {
		return nil, fmt.Errorf("malformed metadata")
	}
	fields := make(map[string]string, len(matches))
	allowed := make(map[string]bool, len(required))
	for _, key := range required {
		allowed[key] = true
	}
	for i, match := range matches {
		key := raw[match[2]:match[3]]
		_, duplicate := fields[key]
		if !allowed[key] || duplicate {
			return nil, fmt.Errorf("unknown or duplicate metadata field %q", key)
		}
		end := len(raw)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		fields[key] = strings.TrimSpace(raw[match[1]:end])
	}
	for _, key := range required {
		if fields[key] == "" {
			return nil, fmt.Errorf("missing metadata field %s", key)
		}
	}
	return fields, nil
}

// validSourceList accepts the provenance a claim may cite: transcript
// sequence markers ("t<seq>") or a memory citation ("mem:<path>:<byte-
// offset>") naming the memory line a repair run adopted the claim from. The
// memory form shares the grammar with the ledger's src= field through
// claims.ValidMemorySource.
func validSourceList(raw string) bool {
	for _, source := range strings.Split(raw, ",") {
		source = strings.TrimSpace(source)
		if !sourceIDPattern.MatchString(source) && !claims.ValidMemorySource(source) {
			return false
		}
	}
	return raw != ""
}

func checkSemanticClaims(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	written []string,
) ([]claimSrcDeferral, error) {
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list semantic claims: %w", err)
	}
	before := make(map[string]claims.Claim)
	for runtimePath, raw := range baseline {
		if semanticCapFor(runtimePath) == 0 {
			continue
		}
		parsed, _, err := claims.ParseClaims(raw)
		if err != nil {
			return nil, err
		}
		for _, claim := range parsed {
			before[claim.ID] = claim
		}
	}
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return nil, fmt.Errorf("load historical claim IDs: %w", err)
	}
	historical := make(map[string]bool)
	for _, id := range historicalClaimIDPattern.FindAllString(history+"\n"+baseline[workingLedgerRuntimePath], -1) {
		historical[id] = true
	}
	changed := make(map[string]bool, len(written))
	for _, runtimePath := range written {
		changed[runtimePath] = true
	}
	seen := make(map[string]string)
	var deferred []claimSrcDeferral
	for relative := range sizes {
		runtimePath := memoryRuntimePrefix + relative
		raw, existed, err := loader.ReadFileBytes(ctx, relative)
		if err != nil {
			return nil, fmt.Errorf("read semantic claims %s: %w", relative, err)
		}
		if !existed {
			return nil, fmt.Errorf("semantic claim inventory file %s is missing", relative)
		}
		if previous, present := baseline[runtimePath]; !present || previous != string(raw) {
			changed[runtimePath] = true
		}
		if limit := semanticCapFor(runtimePath); changed[runtimePath] && limit > 0 &&
			len(raw) > limit {
			return nil, fmt.Errorf(
				"changed semantic file %s is over cap (%d>%d)",
				relative,
				len(raw),
				limit,
			)
		}
		parsed, _, err := claims.ParseClaims(string(raw))
		if err != nil {
			return nil, err
		}
		if len(parsed) > 0 && !semanticClaimPath(runtimePath) {
			return nil, fmt.Errorf(
				"durable claims in %s cannot be indexed; semantic claim files must end in .md",
				relative,
			)
		}
		for _, claim := range parsed {
			if previous, ok := seen[claim.ID]; ok {
				return nil, fmt.Errorf(
					"duplicate claim ID %s in %s and %s",
					claim.ID,
					previous,
					relative,
				)
			}
			seen[claim.ID] = relative
			old, existedBefore := before[claim.ID]
			if !existedBefore && historical[claim.ID] {
				return nil, fmt.Errorf(
					"claim ID %s was previously used and cannot be reused",
					claim.ID,
				)
			}
			if existedBefore && changed[runtimePath] {
				for _, source := range old.Src {
					found := false
					for _, current := range claim.Src {
						if current == source {
							found = true
						}
					}
					if !found {
						return nil, fmt.Errorf(
							"claim %s removed existing provenance %s",
							claim.ID,
							source,
						)
					}
				}
				if claim.Rev < old.Rev {
					return nil, fmt.Errorf("claim %s moved revision backwards", claim.ID)
				}
			}
		}
		if !changed[runtimePath] {
			continue
		}
		// The apply boundary defers src-invalid new claims individually
		// (issue #390): a one-claim citation failure must not discard every
		// valid claim in the pass, so the item is removed from the written
		// file and named in the run result. Everything the baseline already
		// knows as durable stays protected: a src-invalid rewrite of an
		// existing claim fails the run whole, as does any other refusal.
		content := string(raw)
		var fileDeferrals []claimSrcDeferral
		for _, item := range workingItems(content) {
			if relative == semanticProjectsRelativePath && validTopicIndex(item) {
				continue
			}
			if validationErr := validateClaimItem(item); validationErr != nil {
				cleaned, deferral, ok := deferSrcInvalidClaim(
					content,
					item,
					validationErr,
					before,
				)
				if !ok {
					return nil, fmt.Errorf(
						"invalid written semantic claim in %s: %w",
						relative,
						validationErr,
					)
				}
				content = cleaned
				deferral.Path = relative
				fileDeferrals = append(fileDeferrals, deferral)
			}
		}
		if len(fileDeferrals) > 0 {
			if err := loader.WriteFileBytes(ctx, relative, []byte(content)); err != nil {
				return nil, fmt.Errorf("defer src-invalid claims in %s: %w", relative, err)
			}
			deferred = append(deferred, fileDeferrals...)
		}
	}
	entries, err := appendedWorkingLedgerEntries(ctx, loader, baseline, "semantic-memory run")
	if err != nil {
		return nil, err
	}
	recorded := make(map[string]bool)
	for _, entry := range entries {
		switch entry.Outcome {
		case claims.OutcomeSuperseded, claims.OutcomeRetracted, claims.OutcomeResolved:
			if !claimIDPattern.MatchString(entry.TargetID) || strings.TrimSpace(entry.Note) == "" {
				return nil, fmt.Errorf(
					"claim lifecycle entry requires the old claim ID and a reason",
				)
			}
			recorded[entry.TargetID] = true
		}
	}
	for id := range before {
		if _, remains := seen[id]; !remains && !recorded[id] {
			return nil, fmt.Errorf(
				"removed durable claim %s has no lifecycle ledger record",
				id,
			)
		}
	}
	return deferred, nil
}

// deferredClaimIndex maps deferrals by claim ID for the exit-target check.
func deferredClaimIndex(deferrals []claimSrcDeferral) map[string]claimSrcDeferral {
	if len(deferrals) == 0 {
		return nil
	}
	index := make(map[string]claimSrcDeferral, len(deferrals))
	for _, deferral := range deferrals {
		index[deferral.ID] = deferral
	}
	return index
}

// deferredClaimMetadata renders the run result's deferral disclosure: every
// deferred claim named with its bounded refusal, up to a fixed list bound,
// with the overflow disclosed as a count.
func deferredClaimMetadata(deferrals []claimSrcDeferral) map[string]string {
	if len(deferrals) == 0 {
		return nil
	}
	named := min(len(deferrals), semanticDeferredClaimListLimit)
	disclosures := make([]string, 0, named)
	for _, deferral := range deferrals[:named] {
		disclosures = append(
			disclosures,
			fmt.Sprintf("%s in %s: %s", deferral.ID, deferral.Path, deferral.Reason),
		)
	}
	metadata := map[string]string{
		"deferred_claim_count": strconv.Itoa(len(deferrals)),
		"deferred_claims":      strings.Join(disclosures, "; "),
	}
	if omitted := len(deferrals) - named; omitted > 0 {
		metadata["deferred_claims_omitted"] = strconv.Itoa(omitted)
	}
	return metadata
}

func validTopicIndex(item string) bool {
	match := topicIndexPattern.FindStringSubmatch(item)
	if match == nil || match[1] != match[3] || strings.TrimSpace(match[2]) == "" {
		return false
	}
	parsed, _, err := claims.ParseClaims(item)
	return err == nil && len(parsed) == 0
}

type conversionOpenLoop struct {
	ID      string
	Text    string
	Trigger string
	Owner   string
	Src     string
	Item    string
}

func parseOpenLoops(content string) ([]conversionOpenLoop, error) {
	var loops []conversionOpenLoop
	seen := make(map[string]bool)
	for _, item := range workingItems(content) {
		if err := validateLoopItem(item); err != nil {
			return nil, err
		}
		match := loopHeadPattern.FindStringSubmatch(item)
		if seen[match[1]] {
			return nil, fmt.Errorf("duplicate open-loop ID %s", match[1])
		}
		seen[match[1]] = true
		fields, err := itemMetadata(item, "trigger", "owner", "src")
		if err != nil {
			return nil, err
		}
		text := match[2]
		if open := strings.Index(text, "{"); open >= 0 {
			text = text[:open]
		}
		loops = append(
			loops,
			conversionOpenLoop{
				ID:      match[1],
				Text:    strings.TrimSpace(text),
				Trigger: fields["trigger"],
				Owner:   fields["owner"],
				Src:     fields["src"],
				Item:    item,
			},
		)
	}
	return loops, nil
}

func conversionClaimIDSummary(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) (string, error) {
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return "", fmt.Errorf("load historical claim IDs: %w", err)
	}
	var source strings.Builder
	source.WriteString(history)
	for _, content := range baseline {
		source.WriteByte('\n')
		source.WriteString(content)
	}
	highest := make(map[string]int)
	for _, id := range historicalClaimIDPattern.FindAllString(source.String(), -1) {
		fields := strings.Split(id, "-")
		number, err := strconv.Atoi(fields[2])
		if err != nil {
			return "", fmt.Errorf("invalid historical claim ID %s: %w", id, err)
		}
		if number > highest[fields[1]] {
			highest[fields[1]] = number
		}
	}
	years := make([]string, 0, len(highest))
	for year := range highest {
		years = append(years, year)
	}
	sort.Strings(years)
	var lines []string
	for _, year := range years {
		lines = append(
			lines,
			fmt.Sprintf("%s: allocate above c-%s-%04d", year, year, highest[year]),
		)
	}
	if len(lines) == 0 {
		return "No prior claim IDs are recorded. Start at c-YYYY-0001 for the current year.", nil
	}
	return strings.Join(lines, "\n"), nil
}
