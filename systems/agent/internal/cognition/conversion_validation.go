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

// claimParseRefusalError is the typed parse-stage refusal: a written item
// failed the stable-ID-and-statement grammar before any validated claim
// exists to check further (issue #394). The refusal fires on the raw item,
// which is why the ids it still recognized are carried with it: the apply
// boundary defers this class for a claim attempt - an item with a claim head
// or the canonical metadata span - by dropping the item's own bytes and
// naming them by byte offset, unless one of those ids is a claim the
// baseline knows, which stays a mangled rewrite of durable knowledge and
// fails the run whole. The message is the fixed grammar refusal, so a
// disclosed deferral reason is bounded by construction.
type claimParseRefusalError struct{ ids []string }

func (claimParseRefusalError) Error() string {
	return "claim requires a stable ID and statement"
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
	deferred map[string]claimDeferral,
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
					// The apply step deferred this claim - its src citations
					// alone failed the grammar (#390), or it was written so
					// badly its ID stayed but the statement did not (#394) -
					// and removed it from the written file. The exit still
					// records the completed working-memory exit it names, and
					// the run result discloses the deferral.
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
		ids := make([]string, 0, len(parsed))
		for _, claim := range parsed {
			ids = append(ids, claim.ID)
		}
		return claimParseRefusalError{ids: ids}
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

// claimDeferral records one written claim item the conversion apply step
// removed from the file instead of failing the run: a never-before-seen
// claim whose src citations alone failed the grammar (issue #390), or a
// parse-invalid item - one that never gained a stable ID and statement -
// dropped at the raw-item level (issue #394). The deferral is disclosed in
// the run result so the pass's valid claims still apply and the extraction
// checkpoint still advances.
type claimDeferral struct {
	// ID is the removed item's stable claim ID. A parse-invalid item that
	// carries no usable ID defers with ID empty, and is then not keyed for
	// the exit-target check because no ledger exit can name it.
	ID string
	// Name is the run-result disclosure name: the claim ID for an ID-keyed
	// removal, else the dropped item's byte offset in the written file.
	Name string
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

// deferSrcInvalidClaim decides whether one src-invalid claim item is
// deferred, and its deferral record is the disclosure: the claim ID keys the
// exit-target check and names the deferral in the run result. It defers only
// a refusal of the typed src-grammar error on a newly introduced claim with
// one well-formed head - a claim the baseline semantic files know is a
// rewrite of durable knowledge and fails the run instead; anything else
// keeps today's whole-pass failure.
func deferSrcInvalidClaim(
	item string,
	validationErr error,
	before map[string]claims.Claim,
) (claimDeferral, bool) {
	var refusal claimSrcRefusalError
	if !errors.As(validationErr, &refusal) {
		return claimDeferral{}, false
	}
	parsed, _, err := claims.ParseClaims(item)
	if err != nil || len(parsed) != 1 {
		return claimDeferral{}, false
	}
	// A single well-formed claim head keeps an ID worth keying the exit
	// check and the disclosure on; the removal itself is the item's own byte
	// range, exactly like the raw-item deferral.
	if !claimIDPattern.MatchString(parsed[0].ID) {
		return claimDeferral{}, false
	}
	if _, existed := before[parsed[0].ID]; existed {
		return claimDeferral{}, false
	}
	return claimDeferral{
		ID:   parsed[0].ID,
		Name: parsed[0].ID,
		Reason: claimSrcRefusalError{
			src: boundedDeferredSrc(refusal.src),
		}.Error(),
	}, true
}

// deferParseInvalidItem decides whether one parse-invalid item is deferred.
// It defers only the typed parse-stage refusal (#394) on a claim attempt -
// an item that carries a claim head or the canonical metadata span the claim
// grammar reads - and only when none of the item's claim heads is an ID the
// baseline knows: a baseline-known ID here is a mangled rewrite of durable
// knowledge and keeps today's whole-run refusal. Bare bullets and
// index-shaped prose name no claim at all, so they keep today's whole-run
// refusal too, protecting the topic-index integrity guardrail. The loop
// names the record by the item's own byte offset, because with no stable ID
// and statement there is nothing else to name.
func deferParseInvalidItem(
	item string,
	validationErr error,
	before map[string]claims.Claim,
) (claimDeferral, bool) {
	var refusal claimParseRefusalError
	if !errors.As(validationErr, &refusal) {
		return claimDeferral{}, false
	}
	for _, id := range refusal.ids {
		if _, existed := before[id]; existed {
			return claimDeferral{}, false
		}
	}
	if len(refusal.ids) == 0 && !hasCanonicalClaimMetadata(item) {
		return claimDeferral{}, false
	}
	var id string
	if len(refusal.ids) > 0 {
		id = refusal.ids[0]
	}
	return claimDeferral{
		ID:     id,
		Reason: refusal.Error(),
	}, true
}

// hasCanonicalClaimMetadata reports whether the item carries the canonical
// claim metadata span with every claim field, the shape of a claim the
// extraction model attempted but broke at the ID/statement stage.
func hasCanonicalClaimMetadata(item string) bool {
	_, err := itemMetadata(item, "status", "kind", "confidence", "src", "rev")
	return err == nil
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
) ([]claimDeferral, error) {
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
	var deferred []claimDeferral
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
		// The apply boundary defers one-item failures that cannot cost
		// durable knowledge (issues #390, #394): a one-claim citation failure
		// or a parse-invalid claim attempt must not discard every valid
		// claim in the pass, so the item is removed from the written file and
		// named in the run result. Everything the baseline already knows as
		// durable stays protected: a refusal on a rewrite of an existing
		// claim fails the run whole, as does any refusal on an item that
		// never claimed to be a claim.
		content := string(raw)
		var fileDeferrals []claimDeferral
		var drops []workingItemSpan
		// One scan decides every item against the file as written; the
		// single splice afterwards keeps each disclosed byte offset a real
		// offset in that written file, whatever the other removals did.
		for _, span := range workingItemSpans(content) {
			item := span.Item
			if relative == semanticProjectsRelativePath && validTopicIndex(item) {
				continue
			}
			if validationErr := validateClaimItem(item); validationErr != nil {
				deferral, ok := deferSrcInvalidClaim(item, validationErr, before)
				// A src refusal on one well-formed claim head is ID-keyed;
				// anything else gets its one chance at the raw-item
				// parse-stage deferral.
				if !ok {
					deferral, ok = deferParseInvalidItem(item, validationErr, before)
				}
				if !ok {
					return nil, fmt.Errorf(
						"invalid written semantic claim in %s: %w",
						relative,
						validationErr,
					)
				}
				deferral.Path = relative
				if deferral.Name == "" {
					// The parse-stage record carries no ID to name, so the
					// run result names the dropped item's own byte offset.
					deferral.Name = fmt.Sprintf("byte %d", span.Head)
				}
				fileDeferrals = append(fileDeferrals, deferral)
				drops = append(drops, span)
			}
		}
		if len(drops) > 0 {
			content = dropWorkingItemSpans(content, drops)
			if err := loader.WriteFileBytes(ctx, relative, []byte(content)); err != nil {
				return nil, fmt.Errorf("defer invalid claims in %s: %w", relative, err)
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

// deferredClaimIndex maps the deferrals that carry a claim ID by that ID for
// the exit-target check; raw-item deferrals have no ID to key.
func deferredClaimIndex(deferrals []claimDeferral) map[string]claimDeferral {
	if len(deferrals) == 0 {
		return nil
	}
	index := make(map[string]claimDeferral, len(deferrals))
	for _, deferral := range deferrals {
		if deferral.ID != "" {
			index[deferral.ID] = deferral
		}
	}
	return index
}

// deferredClaimMetadata renders the run result's deferral disclosure: every
// deferred item named with its bounded refusal, up to a fixed list bound,
// with the overflow disclosed as a count. An ID-keyed removal names the
// claim ID; a raw-item drop names the item's byte offset.
func deferredClaimMetadata(deferrals []claimDeferral) map[string]string {
	if len(deferrals) == 0 {
		return nil
	}
	named := min(len(deferrals), semanticDeferredClaimListLimit)
	disclosures := make([]string, 0, named)
	for _, deferral := range deferrals[:named] {
		disclosures = append(
			disclosures,
			fmt.Sprintf("%s in %s: %s", deferral.Name, deferral.Path, deferral.Reason),
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
