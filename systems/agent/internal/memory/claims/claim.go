// Package claims implements the durable claim format and the conversion
// ledger used by the memory conversion pass (DESIGN.md §2.1 and §3.3).
//
// A claim is one markdown list entry with a stable ID and an inline metadata
// span, so that the embed scanner's section chunking indexes it as a single
// unit, a cognition model can edit it with edit_file without disturbing its
// neighbours, and grep can answer provenance questions:
//
//   - [c-2026-0412] The <project X> deploy target is <Y>.
//     {status: active, kind: fact, confidence: confirmed, src: t4312, t4391, rev: 2026-10-11}
//
// Claims must be self-contained because retrieval returns them as isolated
// chunks.
//
// Parsing is tolerant: recognised claim heads and their metadata are lifted
// into Claim values, and every other source line is kept verbatim in an
// Unparsed block so legacy content survives a round trip unchanged.
package claims

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Status is the lifecycle state of a claim.
type Status string

const (
	// StatusActive marks a claim that is the current best belief.
	StatusActive Status = "active"
	// StatusConflicted marks a claim that contradicts another unresolved claim.
	StatusConflicted Status = "conflicted"
	// StatusSupersededPrefix prefixes a transient supersession marker. The
	// full value is "superseded-by:c-YYYY-NNNN".
	StatusSupersededPrefix = "superseded-by:"
)

// Kind is the epistemic label of a claim.
type Kind string

const (
	// KindFact marks a claim asserted directly by a source.
	KindFact Kind = "fact"
	// KindInference marks a claim derived rather than observed.
	KindInference Kind = "inference"
	// KindPreference marks a claim about a preference rather than a fact.
	KindPreference Kind = "preference"
)

// Confidence is the strength of evidence behind a claim.
type Confidence string

const (
	// ConfidenceConfirmed marks a claim backed by direct evidence.
	ConfidenceConfirmed Confidence = "confirmed"
	// ConfidenceLikely marks a claim that is probably true.
	ConfidenceLikely Confidence = "likely"
	// ConfidenceUncertain marks a claim whose evidence is weak.
	ConfidenceUncertain Confidence = "uncertain"
)

// Claim is one durable, self-contained statement with provenance.
type Claim struct {
	// ID is the stable identifier "c-YYYY-NNNN", assigned at write time and
	// never reused.
	ID string
	// Text is the claim body without the leading list marker and without any
	// trailing metadata span.
	Text string
	// Status is the lifecycle state. The empty status renders and parses as
	// StatusActive so that hand-written entries stay terse.
	Status Status
	// Kind is the epistemic label.
	Kind Kind
	// Confidence is the strength of evidence.
	Confidence Confidence
	// Src lists transcript sequence markers ("t<seq>") evidencing the claim.
	Src []string
	// Rev is the date of the last touch, "YYYY-MM-DD".
	Rev string
}

// SupersededBy returns the replacement claim ID when the claim is marked
// superseded.
func (c Claim) SupersededBy() (string, bool) {
	value, ok := strings.CutPrefix(string(c.Status), StatusSupersededPrefix)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

// EffectiveStatus returns the status that should be rendered, defaulting an
// empty status to StatusActive.
func (c Claim) EffectiveStatus() Status {
	if strings.TrimSpace(string(c.Status)) == "" {
		return StatusActive
	}
	return c.Status
}

// groupedMetadataOrder is the field order RenderClaim emits, chosen so the
// common case reads as "what it is, how sure, from where, when".
var groupedMetadataOrder = []string{"status", "kind", "confidence", "src", "rev"}

// knownMetadataFields is the closed set of keys the parser and renderer agree
// on, both inside a "{…}" group and in the legacy bare "key: value" spans.
var knownMetadataFields = map[string]struct{}{
	"status":     {},
	"kind":       {},
	"confidence": {},
	"src":        {},
	"rev":        {},
	"id":         {},
}

var (
	// metadataGroupPattern matches a single "{…}" metadata span.
	metadataGroupPattern = regexp.MustCompile(`\{([^{}]*)\}`)
	// metadataFieldPattern matches one "key: value" field. The key may repeat,
	// because a source list such as "src: t1, t2" carries several values in
	// one field.
	metadataFieldPattern = regexp.MustCompile(`(?i)(status|kind|confidence|src|rev|id)\s*:`)
	// claimLinePattern is the historical name for the claim head pattern,
	// retained because legacy callers and tests reference it directly.
	claimLinePattern = regexp.MustCompile(`^\s*[-*+]\s+\[(c-\d{4}-\d+)\]\s*(.*)$`)
)

// metadataPair is one "key: value" field lifted from a claim's metadata span.
type metadataPair struct {
	key   string
	value string
}

const claimIndent = "  "

// ParseClaims splits markdown into claims and the unparsed remainder.
//
// A claim starts at a line of the form "- [c-YYYY-NNNN] text" — either with an
// inline "{…}" metadata span or with an indented continuation carrying the
// metadata. Text that is not part of a claim is returned as an Unparsed block,
// with its lines preserved verbatim and in source order, so a parse→render→parse
// round trip is the identity for any markdown, valid claims or not.
func ParseClaims(markdown string) ([]Claim, []Unparsed, error) {
	lines := strings.Split(markdown, "\n")

	var (
		claims   []Claim
		unparsed []Unparsed
		pending  []string
	)

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		unparsed = append(unparsed, Unparsed{
			Lines:  append([]string(nil), pending...),
			Before: len(claims),
		})
		pending = pending[:0]
	}

	for i := 0; i < len(lines); i++ {
		match := claimLinePattern.FindStringSubmatch(lines[i])
		if match == nil {
			pending = append(pending, lines[i])
			continue
		}

		text, metadata := splitInlineMetadata(match[2])
		claim := Claim{
			ID:   match[1],
			Text: strings.TrimSpace(text),
		}

		continuation, last := consumeMetadata(lines, i+1)
		i = last
		applyMetadata(&claim, parseMetadata(metadata+"\n"+continuation))

		flushPending()
		claims = append(claims, claim)
	}

	flushPending()
	return claims, unparsed, nil
}

// splitInlineMetadata separates a trailing "{…}" metadata span from claim text.
// Only the last balanced group is treated as metadata, so a claim may mention
// braces in its body.
func splitInlineMetadata(raw string) (text, metadata string) {
	trimmed := strings.TrimRight(raw, " \t")
	if !strings.HasSuffix(trimmed, "}") {
		return raw, ""
	}
	open := strings.LastIndex(trimmed, "{")
	if open < 0 || strings.Contains(trimmed[open+1:len(trimmed)-1], "}") {
		return raw, ""
	}
	group := trimmed[open:]
	if !metadataGroupLooksValid(group) {
		return raw, ""
	}
	return trimmed[:open], group
}

// metadataGroupLooksValid reports whether a "{…}" span contains at least one
// known metadata field, which keeps prose braces out of the metadata path.
func metadataGroupLooksValid(group string) bool {
	for _, field := range strings.Split(strings.Trim(group, "{}"), ",") {
		key, _, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		if _, known := knownMetadataFields[normalizeMetadataKey(key)]; known {
			return true
		}
	}
	return false
}

// consumeMetadata walks the lines after a claim head and collects the ones that
// belong to that claim's metadata. A continuation keeps its source position:
// the walk stops at the first blank line, the next claim head, or a line that
// is neither indented nor a bare "key: value" span, so the parser never
// swallows a neighbouring unparsed bullet or paragraph.
//
// It returns the joined continuation text and the index of the last consumed
// line (start-1 when nothing was consumed).
func consumeMetadata(lines []string, start int) (string, int) {
	last := start - 1
	var buffered []string

	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			break
		}
		if claimLinePattern.MatchString(line) {
			break
		}
		if !isIndented(line) && !hasMetadataSpan(line) {
			break
		}
		buffered = append(buffered, line)
		last = i
	}

	return strings.Join(buffered, "\n"), last
}

// hasMetadataSpan reports whether a line contains a metadata span, either as a
// "{…}" group or as a bare "key: value" span.
func hasMetadataSpan(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.Contains(trimmed, "{") && metadataGroupPattern.MatchString(trimmed) {
		return true
	}
	return metadataFieldPattern.MatchString(trimmed)
}

// isIndented reports whether a line starts with whitespace.
func isIndented(line string) bool {
	return line != strings.TrimLeft(line, " \t")
}

// parseMetadata extracts the known metadata fields from a raw continuation
// block. It understands the canonical "{a: b, c: d}" group as well as legacy
// "key: value" spans, including several span fields on one line.
func parseMetadata(raw string) []metadataPair {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	spans := []string{}
	for _, group := range metadataGroupPattern.FindAllString(raw, -1) {
		spans = append(spans, strings.Trim(group, "{}"))
	}
	spans = append(spans, metadataGroupPattern.ReplaceAllString(raw, " "))

	var pairs []metadataPair
	seen := make(map[string]struct{})
	appendPair := func(key, value string) {
		key = normalizeMetadataKey(key)
		if key == "" {
			return
		}
		if _, duplicate := seen[key]; duplicate && key != "src" {
			return
		}
		seen[key] = struct{}{}
		pairs = append(pairs, metadataPair{key: key, value: strings.TrimSpace(value)})
	}

	for _, span := range spans {
		for _, field := range splitMetadataFields(span) {
			key, value, ok := strings.Cut(field, ":")
			if !ok {
				continue
			}
			appendPair(key, value)
		}
	}
	return pairs
}

// splitMetadataFields splits a metadata span into fields. A field boundary is a
// comma (or a line break) that is followed by a known "key:" — not by a value
// such as the next "t4312" of a source list.
func splitMetadataFields(span string) []string {
	var (
		fields  []string
		current strings.Builder
	)
	flush := func() {
		field := strings.TrimSpace(current.String())
		current.Reset()
		if field != "" {
			fields = append(fields, field)
		}
	}

	for i := 0; i < len(span); i++ {
		switch char := span[i]; char {
		case '\n', '\r':
			flush()
		case ',':
			if key, ok := pendingMetadataKey(strings.TrimSpace(current.String())); ok {
				// "rev: 2026-10-11," — the comma ends the field.
				current.WriteString(":")
				_ = key
				flush()
				continue
			}
			rest := span[i+1:]
			next := strings.TrimSpace(rest)
			if next != "" && startsKnownMetadataKey(rest) {
				flush()
				continue
			}
			current.WriteByte(char)
		default:
			current.WriteByte(char)
		}
	}
	flush()
	return fields
}

// pendingMetadataKey reports whether the accumulated field text is exactly a
// known key still missing its value (the "rev:" case).
func pendingMetadataKey(field string) (string, bool) {
	key, value, ok := strings.Cut(field, ":")
	if !ok || strings.TrimSpace(value) != "" {
		return "", false
	}
	normalized := normalizeMetadataKey(key)
	if normalized == "" {
		return "", false
	}
	return normalized, true
}

// startsKnownMetadataKey reports whether text begins with a "key:" for a known
// metadata field.
func startsKnownMetadataKey(text string) bool {
	trimmed := strings.TrimLeft(text, " \t")
	match := metadataFieldPattern.FindString(trimmed)
	if match == "" || !strings.HasPrefix(trimmed, match) {
		return false
	}
	return normalizeMetadataKey(strings.TrimSuffix(match, ":")) != ""
}

func normalizeMetadataKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.TrimLeft(key, "-*+ \t")
	key = strings.TrimSuffix(key, ":")
	if _, ok := knownMetadataFields[key]; !ok {
		return ""
	}
	return key
}

func applyMetadata(claim *Claim, pairs []metadataPair) {
	var srcValue string
	for _, pair := range pairs {
		value := strings.TrimSpace(pair.value)
		switch pair.key {
		case "status":
			if value != "" {
				claim.Status = Status(value)
			}
		case "kind":
			if value != "" {
				claim.Kind = Kind(value)
			}
		case "confidence":
			if value != "" {
				claim.Confidence = Confidence(value)
			}
		case "src":
			srcValue = value
		case "rev":
			if value != "" {
				claim.Rev = value
			}
		}
	}
	claim.Src = parseSourceIDs(srcValue)
}

// parseSourceIDs splits a src value into transcript sequence markers. Both
// "t4312, t4391" and the legacy "t4391..t4414" range form are accepted.
func parseSourceIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var out []string
	seen := make(map[string]struct{})
	for _, field := range splitSourceList(raw) {
		field = stripRangeEndpoints(field)
		if field == "" {
			continue
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}

// splitSourceList splits a src value on its separator characters, keeping only
// transcript markers ("t<digits>") and dropping prose around them.
func splitSourceList(raw string) []string {
	var (
		tokens  []string
		current strings.Builder
	)
	flush := func() {
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token != "" {
			tokens = append(tokens, token)
		}
	}

	for _, char := range raw {
		switch char {
		case ',', ';', '|', ' ', '\t':
			flush()
		default:
			current.WriteRune(char)
		}
	}
	flush()

	markers := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if marker, ok := forceTranscriptMarker(token); ok {
			markers = append(markers, marker)
		}
	}
	return markers
}

// forceTranscriptMarker strips range separators and surrounding punctuation
// from a source token, returning "" when it is not a transcript marker.
func forceTranscriptMarker(token string) (string, bool) {
	token = strings.Trim(token, `"'()[]{}`+"\u2026")
	token = strings.TrimPrefix(token, "t")
	token = strings.Trim(token, ".")
	if token == "" {
		return "", false
	}
	for _, char := range token {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	return "t" + token, true
}

// stripRangeEndpoints lifts the endpoints out of a "t4312..t4391" range, which
// is the compact form DESIGN.md §3.3 uses for a ledger seq range.
func stripRangeEndpoints(field string) string {
	parts := strings.SplitN(field, "..", 2)
	if len(parts) == 1 {
		return field
	}
	return parts[0]
}

// RenderClaim renders a claim in the canonical claim format: the text line
// followed by one two-space-indented metadata group, always with a single
// trailing newline.
func RenderClaim(claim Claim) string {
	var builder strings.Builder
	builder.WriteString("- [")
	builder.WriteString(claim.ID)
	builder.WriteString("] ")
	builder.WriteString(strings.TrimSpace(claim.Text))
	builder.WriteString("\n")
	builder.WriteString(claimIndent)
	builder.WriteString("{")
	for i, key := range groupedMetadataOrder {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(renderMetadataValue(claim, key))
	}
	builder.WriteString("}\n")
	return builder.String()
}

func renderMetadataValue(claim Claim, key string) string {
	switch key {
	case "status":
		return string(claim.EffectiveStatus())
	case "kind":
		if claim.Kind == "" {
			return string(KindFact)
		}
		return string(claim.Kind)
	case "confidence":
		if claim.Confidence == "" {
			return string(ConfidenceLikely)
		}
		return string(claim.Confidence)
	case "src":
		return strings.Join(claim.Src, ", ")
	case "rev":
		return strings.TrimSpace(claim.Rev)
	default:
		return ""
	}
}

// Unparsed is a block of markdown that is not claim-formatted. Its lines are
// preserved verbatim so legacy content survives a round trip.
type Unparsed struct {
	// Lines holds the block's source lines, without trailing newlines.
	Lines []string
	// Before is the index of the claim this block precedes, so a file that
	// interleaves prose and claims can be rebuilt in its original order. A
	// value equal to len(claims) means the block trails every claim.
	Before int
}

// String returns the block exactly as it appeared in the source markdown.
func (u Unparsed) String() string {
	return strings.Join(u.Lines, "\n")
}

// RenderMarkdown rebuilds a document from its parsed parts, interleaving the
// claims and the unparsed blocks in their original order. For any markdown,
// RenderMarkdown(ParseClaims(markdown)) reproduces it byte for byte.
func RenderMarkdown(claims []Claim, unparsed []Unparsed) string {
	lines := make([]string, 0, len(claims)*2+len(unparsed))
	blockIndex := 0

	appendBlock := func(block Unparsed) {
		lines = append(lines, block.Lines...)
	}
	appendClaim := func(claim Claim) {
		rendered := strings.TrimSuffix(RenderClaim(claim), "\n")
		if rendered == "" {
			return
		}
		lines = append(lines, strings.Split(rendered, "\n")...)
	}

	for i := range claims {
		for blockIndex < len(unparsed) && unparsed[blockIndex].Before <= i {
			appendBlock(unparsed[blockIndex])
			blockIndex++
		}
		appendClaim(claims[i])
	}
	for ; blockIndex < len(unparsed); blockIndex++ {
		appendBlock(unparsed[blockIndex])
	}

	return strings.Join(lines, "\n")
}

// NextClaimID allocates the next claim ID for year, given the claims already
// present in a file. The counter is derived from the highest existing
// "c-<year>-NNNN" value so IDs are monotonic and never reused within a year.
func NextClaimID(year int, existing []Claim) string {
	highest := 0
	prefix := fmt.Sprintf("c-%04d-", year)
	for _, claim := range existing {
		id := strings.TrimSpace(claim.ID)
		counter, ok := strings.CutPrefix(id, prefix)
		if !ok {
			continue
		}
		value, err := strconv.Atoi(counter)
		if err != nil || value <= 0 {
			continue
		}
		if value > highest {
			highest = value
		}
	}
	return fmt.Sprintf("%s%04d", prefix, highest+1)
}
