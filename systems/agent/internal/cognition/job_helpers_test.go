package cognition

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCompactCognitionTextUsesRuneBoundaries(t *testing.T) {
	t.Parallel()

	withinLimit := strings.Repeat("界", cognitionSummaryLimit)
	if got := compactCognitionText(withinLimit); got != withinLimit {
		t.Fatalf(
			"compactCognitionText() changed text within limit: len=%d",
			utf8.RuneCountInString(got),
		)
	}

	overLimit := strings.Repeat("界", cognitionSummaryLimit+1)
	got := compactCognitionText(overLimit)
	if !utf8.ValidString(got) {
		t.Fatalf("compactCognitionText() returned invalid UTF-8: %q", got)
	}
	if gotRunes, wantRunes := utf8.RuneCountInString(got), cognitionSummaryLimit; gotRunes != wantRunes {
		t.Fatalf("compactCognitionText() rune count = %d, want %d", gotRunes, wantRunes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("compactCognitionText() = %q, want ellipsis suffix", got)
	}
}
