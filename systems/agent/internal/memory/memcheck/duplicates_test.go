package memcheck

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// almostEqual compares two float scores with enough slack for one rounding
// step, so a boundary pinned by a fixture is judged by its exact fraction and
// not by floating-point noise.
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestNormalizeClaimText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "folds case, punctuation and whitespace",
			in:   "The, FERRY\tDeparts!!  -before- SUNRISE...",
			want: "the ferry departs before sunrise",
		},
		{
			name: "keeps digits as tokens",
			in:   "platform 9 waits t4312",
			want: "platform 9 waits t4312",
		},
		{
			name: "empty text stays empty",
			in:   "  \t\n ",
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeClaimText(test.in); got != test.want {
				t.Fatalf("NormalizeClaimText(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestTokenOverlap(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want float64
	}{
		{
			name: "identical text after folding is one",
			a:    "The, FERRY\tDeparts!!",
			b:    "the ferry departs",
			want: 1,
		},
		{
			name: "nothing shared is zero",
			a:    "the ferry departs",
			b:    "a kettle whistles",
			want: 0,
		},
		{
			name: "either side empty is zero",
			a:    "the ferry departs",
			b:    "  ",
			want: 0,
		},
		{
			name: "one word apart in nine is the 0.8 boundary",
			a:    "The river ferry departs before sunrise on cold mornings.",
			b:    "The river ferry departs before sunrise on foggy mornings.",
			want: DefaultDuplicateThreshold,
		},
		{
			name: "eleven shared of fourteen is just under",
			a:    "The garden gate swings open whenever our hungry cat wanders past lanterns.",
			b:    "The garden gate swings open while our hungry cat wanders past the quiet lanterns.",
			want: float64(11) / float64(14),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := TokenOverlap(test.a, test.b)
			if !almostEqual(got, test.want) {
				t.Fatalf("TokenOverlap(%q, %q) = %v, want %v", test.a, test.b, got, test.want)
			}
		})
	}
}

// TestCheckDuplicatesOnCorpus is the §7.3 acceptance run: the corpus must stay
// under the rate limit overall, the at-threshold pair must be named with its
// exact boundary score, and the just-under pair must produce nothing.
func TestCheckDuplicatesOnCorpus(t *testing.T) {
	report, err := CheckDuplicates("testdata", DefaultDuplicateOptions())
	if err != nil {
		t.Fatalf("CheckDuplicates(testdata) error = %v", err)
	}

	if !almostEqual(report.Rate, float64(1)/float64(30)) {
		t.Fatalf(
			"corpus rate = %v, want 1 flagged pair over 30 compared pairs",
			report.Rate,
		)
	}
	if report.Rate >= DuplicateRateLimit {
		t.Fatalf("corpus rate %v is not under the %v limit", report.Rate, DuplicateRateLimit)
	}
	if report.Claims != 12 {
		t.Fatalf("claims counted = %d, want 12", report.Claims)
	}
	if report.ComparedPairs != 30 {
		t.Fatalf("compared pairs = %d, want 30", report.ComparedPairs)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("flagged pairs = %d (%v), want exactly the at-threshold pair",
			len(report.Pairs), report.Pairs)
	}

	wantFiles := []struct {
		path          string
		claims        int
		comparedPairs int
		pairs         int
		rate          float64
	}{
		{"semantic/facts.md", 2, 1, 1, 1},
		{"semantic/preferences.md", 2, 1, 0, 0},
		{"semantic/projects.md", 8, 28, 0, 0},
		{"semantic/topics/empty.md", 0, 0, 0, 0},
	}
	if len(report.Files) != len(wantFiles) {
		t.Fatalf("file reports = %d, want %d: %+v", len(report.Files), len(wantFiles), report.Files)
	}
	for i, want := range wantFiles {
		got := report.Files[i]
		if got.Path != want.path {
			t.Fatalf("file report %d path = %q, want %q", i, got.Path, want.path)
		}
		if got.Claims != want.claims || got.ComparedPairs != want.comparedPairs ||
			len(got.Pairs) != want.pairs || !almostEqual(got.Rate, want.rate) {
			t.Fatalf(
				"file report %s = %+v, want %+v",
				want.path,
				got,
				want,
			)
		}
	}

	pair := report.Pairs[0]
	if pair.File != "semantic/facts.md" ||
		pair.FirstID != "c-2026-0101" || pair.SecondID != "c-2026-0102" {
		t.Fatalf("flagged pair names the wrong claims: %+v", pair)
	}
	if !almostEqual(pair.Score, DefaultDuplicateThreshold) {
		t.Fatalf(
			"at-threshold pair score = %v, want exactly %v",
			pair.Score,
			DefaultDuplicateThreshold,
		)
	}
}

// TestCheckDuplicatesTunableOptions pins the measure and threshold as
// parameters: a stricter threshold unflags the boundary pair, and a caller's
// own measure decides what a pair scores.
func TestCheckDuplicatesTunableOptions(t *testing.T) {
	strict, err := CheckDuplicates(
		"testdata",
		DuplicateOptions{Threshold: 0.9, Measure: TokenOverlap},
	)
	if err != nil {
		t.Fatalf("CheckDuplicates threshold 0.9 error = %v", err)
	}
	if len(strict.Pairs) != 0 || strict.Rate != 0 {
		t.Fatalf("threshold 0.9 flagged %+v, want nothing", strict.Pairs)
	}

	everything := func(_, _ string) float64 { return 1 }
	loose, err := CheckDuplicates("testdata", DuplicateOptions{Threshold: 0.5, Measure: everything})
	if err != nil {
		t.Fatalf("CheckDuplicates custom measure error = %v", err)
	}
	if loose.ComparedPairs == 0 || len(loose.Pairs) != loose.ComparedPairs {
		t.Fatalf("custom measure scored %d of %d pairs, want all flagged",
			len(loose.Pairs), loose.ComparedPairs)
	}
	if loose.Rate < DuplicateRateLimit {
		t.Fatalf(
			"fully duplicated corpus rate %v must reach the %v limit",
			loose.Rate,
			DuplicateRateLimit,
		)
	}
}

// TestCheckDuplicatesDefaults applies when options are zero-valued, so a
// caller cannot accidentally run with a threshold that flags everything.
func TestCheckDuplicatesDefaults(t *testing.T) {
	report, err := CheckDuplicates("testdata", DuplicateOptions{})
	if err != nil {
		t.Fatalf("CheckDuplicates zero options error = %v", err)
	}
	if len(report.Pairs) != 1 {
		t.Fatalf("zero options flagged %d pairs, want the default measure's 1", len(report.Pairs))
	}
}

// TestCheckDuplicatesOnDuplicatedRoot proves the rate limit actually fires:
// a root built only of near-duplicate claims reports a rate at the limit and
// names its pairs.
func TestCheckDuplicatesOnDuplicatedRoot(t *testing.T) {
	root := t.TempDir()
	semantic := filepath.Join(root, "semantic")
	if err := os.MkdirAll(semantic, 0o755); err != nil {
		t.Fatalf("mkdir semantic: %v", err)
	}
	duplicated := strings.Join([]string{
		"# Facts",
		"",
		"- [c-2026-0901] The river ferry departs before sunrise on cold mornings.",
		"",
		"- [c-2026-0902] The river ferry departs before sunrise on foggy mornings.",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(semantic, "facts.md"), []byte(duplicated), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := CheckDuplicates(root, DefaultDuplicateOptions())
	if err != nil {
		t.Fatalf("CheckDuplicates(%q) error = %v", root, err)
	}
	if report.Rate != 1 {
		t.Fatalf("fully duplicated root rate = %v, want 1", report.Rate)
	}
	if report.Rate < DuplicateRateLimit {
		t.Fatalf("rate %v must reach the %v limit", report.Rate, DuplicateRateLimit)
	}
	if len(report.Pairs) != 1 ||
		report.Pairs[0].FirstID != "c-2026-0901" || report.Pairs[0].SecondID != "c-2026-0902" {
		t.Fatalf("flagged pair = %+v, want the planted pair", report.Pairs)
	}
}

// TestCheckDuplicatesEmptyRoot reports zero rather than failing, because a
// corpus with no claims has a duplicate rate of zero, not an unknown one.
func TestCheckDuplicatesEmptyRoot(t *testing.T) {
	report, err := CheckDuplicates(t.TempDir(), DefaultDuplicateOptions())
	if err != nil {
		t.Fatalf("CheckDuplicates empty root error = %v", err)
	}
	if len(report.Files) != 0 || report.Claims != 0 || report.Rate != 0 {
		t.Fatalf("empty root report = %+v, want empty", report)
	}
}

// TestCheckDuplicatesSkipsEmptyClaims pins that a claim which parses to no
// text is counted but never compared, so its weight cannot hide duplicates.
func TestCheckDuplicatesSkipsEmptyClaims(t *testing.T) {
	root := t.TempDir()
	semantic := filepath.Join(root, "semantic")
	if err := os.MkdirAll(semantic, 0o755); err != nil {
		t.Fatalf("mkdir semantic: %v", err)
	}
	mixed := strings.Join([]string{
		"# Facts",
		"",
		"- [c-2026-0903] {status: active}",
		"",
		"- [c-2026-0904] The river ferry departs before sunrise on cold mornings.",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(semantic, "facts.md"), []byte(mixed), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	report, err := CheckDuplicates(root, DefaultDuplicateOptions())
	if err != nil {
		t.Fatalf("CheckDuplicates(%q) error = %v", root, err)
	}
	if len(report.Files) != 1 || report.Files[0].Claims != 2 ||
		report.Files[0].ComparedPairs != 0 || len(report.Pairs) != 0 {
		t.Fatalf("uncomparable claim was compared or hidden: %+v", report)
	}
}

// TestCheckDuplicatesMissingRootErrors refuses to measure a path that is not
// a memory root, because a silently empty report would look healthy.
func TestCheckDuplicatesMissingRootErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-root")
	if _, err := CheckDuplicates(missing, DefaultDuplicateOptions()); err == nil {
		t.Fatal("CheckDuplicates on a missing root succeeded, want error")
	}
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := CheckDuplicates(file, DefaultDuplicateOptions()); err == nil {
		t.Fatal("CheckDuplicates on a non-directory succeeded, want error")
	}
}
