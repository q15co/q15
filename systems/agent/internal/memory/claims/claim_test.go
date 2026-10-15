package claims

import (
	"reflect"
	"strings"
	"testing"
)

const validClaimMarkdown = `# Semantic Topics / Sample

## Active Projects

- [c-2026-0412] The <project X> deploy target is <Y>.
  {status: active, kind: fact, confidence: confirmed, src: t4312, t4391, rev: 2026-10-11}

- [c-2026-0001] The user prefers short status updates.
  {status: conflicted, kind: preference, confidence: likely, src: t4401, rev: 2026-10-12}
`

// TestParseRenderParseIsIdentityForValidClaims is the T1 round-trip property:
// parsing rendered claims and rendering them again reproduces the same bytes,
// and the parsed claims compare equal field by field.
func TestParseRenderParseIsIdentityForValidClaims(t *testing.T) {
	markdown := validClaimMarkdown

	for iteration := 0; iteration < 3; iteration++ {
		claims, unparsed, err := ParseClaims(markdown)
		if err != nil {
			t.Fatalf("iteration %d: ParseClaims() error = %v", iteration, err)
		}
		wantBlocks := 3
		if iteration > 0 {
			wantBlocks = 2
		}
		if want := wantBlocks; len(unparsed) != want {
			t.Fatalf(
				"iteration %d: len(unparsed) = %d, want %d (the non-claim prose)",
				iteration,
				len(unparsed),
				want,
			)
		}
		if len(claims) != 2 {
			t.Fatalf("iteration %d: len(claims) = %d, want 2", iteration, len(claims))
		}

		// The claims must be the whole claim content: render them plus the
		// unparsed remainder and the source must survive byte for byte.
		if rebuilt := RenderMarkdown(claims, unparsed); rebuilt != markdown {
			t.Fatalf(
				"iteration %d: round trip changed bytes\n got %q\nwant %q",
				iteration,
				rebuilt,
				markdown,
			)
		}

		var rendered strings.Builder
		for i, claim := range claims {
			if i > 0 {
				rendered.WriteString("\n")
			}
			rendered.WriteString(RenderClaim(claim))
		}

		reparsed, reparsedUnparsed, err := ParseClaims(rendered.String())
		if err != nil {
			t.Fatalf("iteration %d: reparse error = %v", iteration, err)
		}
		// A claims-only document still carries the blank lines between claims
		// and the trailing newline, and the tolerant parser keeps them; what
		// must not happen is a block appearing that the document does not have.
		if want := 2; len(reparsedUnparsed) != want {
			t.Fatalf(
				"iteration %d: reparse unparsed = %d, want %d\n%#v",
				iteration,
				len(reparsedUnparsed),
				want,
				reparsedUnparsed,
			)
		}
		if !reflect.DeepEqual(reparsed, claims) {
			t.Fatalf(
				"iteration %d: round trip changed claims\n got %#v\nwant %#v",
				iteration,
				reparsed,
				claims,
			)
		}

		markdown = rendered.String()
	}
}

func TestParseClaimsReadsEveryField(t *testing.T) {
	claims, unparsed, err := ParseClaims(validClaimMarkdown)
	if err != nil {
		t.Fatalf("ParseClaims() error = %v", err)
	}
	if want := 3; len(unparsed) != want {
		t.Fatalf("len(unparsed) = %d, want %d", len(unparsed), want)
	}

	want := Claim{
		ID:         "c-2026-0412",
		Text:       "The <project X> deploy target is <Y>.",
		Status:     StatusActive,
		Kind:       KindFact,
		Confidence: ConfidenceConfirmed,
		Src:        []string{"t4312", "t4391"},
		Rev:        "2026-10-11",
	}
	if !reflect.DeepEqual(claims[0], want) {
		t.Fatalf("claims[0] = %#v, want %#v", claims[0], want)
	}

	wantSecond := Claim{
		ID:         "c-2026-0001",
		Text:       "The user prefers short status updates.",
		Status:     StatusConflicted,
		Kind:       KindPreference,
		Confidence: ConfidenceLikely,
		Src:        []string{"t4401"},
		Rev:        "2026-10-12",
	}
	if !reflect.DeepEqual(claims[1], wantSecond) {
		t.Fatalf("claims[1] = %#v, want %#v", claims[1], wantSecond)
	}
}

// TestParseClaimsPreservesUnparsedLinesByteForByte is the T1 legacy-survival
// property: prologue, headings, legacy bullets and trailing whitespace are all
// returned verbatim, in order, around the claims that were recognised.
func TestParseClaimsPreservesUnparsedLinesByteForByte(t *testing.T) {
	markdown := strings.Join([]string{
		"# Semantic Projects",
		"",
		"## Active Projects",
		"",
		"- [c-2026-0007] q15 ships from the monorepo. {status: active, kind: fact, " +
			"confidence: confirmed, src: t100, rev: 2026-01-02}",
		"",
		"- legacy bullet with no ID at all",
		"- [not-a-claim-id] another legacy bullet",
		"",
		"## Durable Project Knowledge",
		"",
		"\t- indented legacy bullet	",
		"",
		"trailing paragraph",
		"",
	}, "\n")

	found, unparsed, err := ParseClaims(markdown)
	if err != nil {
		t.Fatalf("ParseClaims() error = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("len(claims) = %d, want 1", len(found))
	}
	if found[0].ID != "c-2026-0007" {
		t.Fatalf("claims[0].ID = %q, want c-2026-0007", found[0].ID)
	}
	if !reflect.DeepEqual(found[0].Src, []string{"t100"}) {
		t.Fatalf("claims[0].Src = %#v, want [t100]", found[0].Src)
	}

	var preserved strings.Builder
	for _, block := range unparsed {
		preserved.WriteString(block.String())
	}
	rebuilt := preserved.String()

	for _, want := range []string{
		"# Semantic Projects",
		"## Active Projects",
		"- legacy bullet with no ID at all",
		"- [not-a-claim-id] another legacy bullet",
		"## Durable Project Knowledge",
		"\t- indented legacy bullet\t",
		"trailing paragraph",
	} {
		if !strings.Contains(rebuilt, want) {
			t.Fatalf("unparsed output lost %q:\n%q", want, rebuilt)
		}
	}

	// The claims cell and the unparsed cells must together account for every
	// non-metadata source line, byte for byte.
	all := preserved.String()
	for _, claim := range found {
		all += claim.Text
	}
	for _, source := range strings.Split(markdown, "\n") {
		if strings.TrimSpace(source) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(source), "{") {
			continue // claim metadata continuation
		}
		if match := claimLinePattern.FindStringSubmatch(source); match != nil {
			continue // recognised claim head
		}
		if !strings.Contains(all, source) {
			t.Fatalf("source line %q survived neither claims nor unparsed", source)
		}
	}
}

func TestParseClaimsEmptyInput(t *testing.T) {
	found, unparsed, err := ParseClaims("")
	if err != nil {
		t.Fatalf("ParseClaims() error = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("len(claims) = %d, want 0", len(found))
	}
	if len(unparsed) != 1 || unparsed[0].String() != "" {
		t.Fatalf("unparsed = %#v, want one empty block", unparsed)
	}
}

func TestParseClaimsAcceptsLegacyStandaloneMetadata(t *testing.T) {
	markdown := "- [c-2026-0031] Legacy entry.\n  status: active, kind: inference, " +
		"confidence: uncertain, src: t9, rev: 2026-02-03\n"

	found, unparsed, err := ParseClaims(markdown)
	if err != nil {
		t.Fatalf("ParseClaims() error = %v", err)
	}
	if len(unparsed) != 1 || unparsed[0].String() != "" {
		// the trailing newline leaves an empty block; anything else is a loss
		if len(unparsed) != 0 {
			t.Fatalf("unparsed = %#v, want no legacy content", unparsed)
		}
	}
	if len(found) != 1 {
		t.Fatalf("len(claims) = %d, want 1", len(found))
	}
	got := found[0]
	if got.Status != StatusActive || got.Kind != KindInference ||
		got.Confidence != ConfidenceUncertain {
		t.Fatalf("metadata not read from standalone spans: %#v", got)
	}
	if !reflect.DeepEqual(got.Src, []string{"t9"}) || got.Rev != "2026-02-03" {
		t.Fatalf("provenance not read from standalone spans: %#v", got)
	}
}

func TestRenderClaimEmitsCanonicalShape(t *testing.T) {
	got := RenderClaim(Claim{
		ID:         "c-2026-0412",
		Text:       "The deploy target is Y.",
		Status:     StatusActive,
		Kind:       KindFact,
		Confidence: ConfidenceConfirmed,
		Src:        []string{"t4312", "t4391"},
		Rev:        "2026-10-11",
	})
	want := "- [c-2026-0412] The deploy target is Y.\n" +
		"  {status: active, kind: fact, confidence: confirmed, src: t4312, t4391, " +
		"rev: 2026-10-11}\n"
	if got != want {
		t.Fatalf("RenderClaim() = %q, want %q", got, want)
	}
}

func TestRenderClaimDefaultsStatusKindAndConfidence(t *testing.T) {
	got := RenderClaim(Claim{ID: "c-2026-0001", Text: "Bare entry."})
	want := "- [c-2026-0001] Bare entry.\n" +
		"  {status: active, kind: fact, confidence: likely, src: , rev: }\n"
	if got != want {
		t.Fatalf("RenderClaim() = %q, want %q", got, want)
	}
}

func TestClaimSupersededBy(t *testing.T) {
	claim := Claim{ID: "c-2026-0100", Status: Status("superseded-by:c-2026-0200")}
	target, ok := claim.SupersededBy()
	if !ok || target != "c-2026-0200" {
		t.Fatalf("SupersededBy() = %q, %v; want c-2026-0200, true", target, ok)
	}

	if _, ok := (Claim{Status: StatusActive}).SupersededBy(); ok {
		t.Fatal("SupersededBy() reported true for an active claim")
	}
	if _, ok := (Claim{Status: Status("superseded-by:")}).SupersededBy(); ok {
		t.Fatal("SupersededBy() reported true for an empty target")
	}
}

// TestNextClaimIDAllocatesBeyondExisting guards the T1 ID-allocation contract:
// the counter comes from the highest existing ID in the same year, ignores
// other years, tolerates junk, and never reuses a value.
func TestNextClaimIDAllocatesBeyondExisting(t *testing.T) {
	tests := []struct {
		name     string
		year     int
		existing []Claim
		want     string
	}{
		{
			name: "empty file starts the year at one",
			year: 2026,
			want: "c-2026-0001",
		},
		{
			name: "continues after the highest ID",
			year: 2026,
			existing: []Claim{
				{ID: "c-2026-0001"},
				{ID: "c-2026-0412"},
				{ID: "c-2026-0010"},
			},
			want: "c-2026-0413",
		},
		{
			name: "ignores other years",
			year: 2026,
			existing: []Claim{
				{ID: "c-2025-0900"},
				{ID: "c-2026-0002"},
			},
			want: "c-2026-0003",
		},
		{
			name: "ignores malformed and blank IDs",
			year: 2026,
			existing: []Claim{
				{ID: ""},
				{ID: "garbage"},
				{ID: "c-2026-abc"},
				{ID: "c-2026-0004"},
			},
			want: "c-2026-0005",
		},
		{
			name: "does not carry the counter into a new year",
			year: 2027,
			existing: []Claim{
				{ID: "c-2026-0412"},
			},
			want: "c-2027-0001",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NextClaimID(test.year, test.existing); got != test.want {
				t.Fatalf("NextClaimID(%d, ...) = %q, want %q", test.year, got, test.want)
			}
		})
	}
}

func TestNextClaimIDIsNeverReusedAcrossSequentialAllocations(t *testing.T) {
	existing := []Claim{}
	seen := map[string]struct{}{}
	for i := 0; i < 5; i++ {
		id := NextClaimID(2026, existing)
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NextClaimID() reused %q", id)
		}
		seen[id] = struct{}{}
		existing = append(existing, Claim{ID: id})
	}
}

func TestParseClaimsPreservesIndentedNonMetadata(t *testing.T) {
	for _, prose := range []string{
		"  legacy paragraph with trailing spaces  ",
		"\t- nested legacy bullet\t",
		"  {prose in braces}",
		"  explanation mentioning src: t17",
		"  {status: active} followed by prose",
	} {
		t.Run(prose, func(t *testing.T) {
			markdown := "- [c-2026-0001] A claim.\n" +
				"  {status: active, kind: fact, confidence: confirmed, src: t1, rev: 2026-10-11}\n" +
				prose + "\n"
			found, unparsed, err := ParseClaims(markdown)
			if err != nil {
				t.Fatalf("ParseClaims() error = %v", err)
			}
			want := []Unparsed{{Lines: []string{prose, ""}, Before: 1}}
			if !reflect.DeepEqual(unparsed, want) {
				t.Fatalf("unparsed = %#v, want %#v", unparsed, want)
			}
			if got := RenderMarkdown(found, unparsed); got != markdown {
				t.Fatalf("RenderMarkdown() = %q, want %q", got, markdown)
			}
		})
	}
}

func TestNextClaimIDUsesHistoricalIDsAcrossTopics(t *testing.T) {
	currentProjects := []Claim{{ID: "c-2026-0003"}}
	currentPreferences := []Claim{{ID: "c-2026-0014"}}
	deleted := []Claim{{ID: "c-2026-0038"}}
	allIssued := append(append(currentProjects, currentPreferences...), deleted...)
	if got := NextClaimID(2026, allIssued); got != "c-2026-0039" {
		t.Fatalf("NextClaimID(all issued IDs) = %q, want c-2026-0039", got)
	}
}
