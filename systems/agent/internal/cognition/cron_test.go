package cognition

import (
	"testing"
	"time"
)

func TestParseCronExprAcceptsSupportedSyntax(t *testing.T) {
	t.Parallel()

	cases := []string{
		"* * * * *",
		"*/5 1,2,3 1-5/2 1,6,12 1-5",
		"0 0 1 * *",
		"15 10 * * 0,6",
	}
	for _, tc := range cases {
		if _, err := parseCronExpr(tc); err != nil {
			t.Fatalf("parseCronExpr(%q) error = %v", tc, err)
		}
	}
}

func TestParseCronExprRejectsUnsupportedSyntax(t *testing.T) {
	t.Parallel()

	cases := []string{
		"@daily",
		"0 0 0 1 * *",
		"TZ=UTC 0 0 * * *",
		"CRON_TZ=UTC 0 0 * * *",
		"0 0 * JAN *",
		"0 0 * * MON",
		"0 0 L * *",
		"0 0 * * 1#2",
		"0 0 * * W",
		"0 0 * * UTC",
		"1/5 * * * *",
	}
	for _, tc := range cases {
		if _, err := parseCronExpr(tc); err == nil {
			t.Fatalf("parseCronExpr(%q) error = nil, want non-nil", tc)
		}
	}
}

func TestCronExprNextMatchesLeapYearAndDayRules(t *testing.T) {
	t.Parallel()

	t.Run("every five minutes", func(t *testing.T) {
		expr, err := parseCronExpr("*/5 * * * *")
		if err != nil {
			t.Fatalf("parseCronExpr() error = %v", err)
		}
		got, ok := expr.next(time.Date(2026, time.April, 6, 10, 1, 30, 0, time.UTC))
		if !ok {
			t.Fatal("next() ok = false, want true")
		}
		want := time.Date(2026, time.April, 6, 10, 5, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("next() = %s, want %s", got, want)
		}
	})

	t.Run("leap day", func(t *testing.T) {
		expr, err := parseCronExpr("0 0 29 2 *")
		if err != nil {
			t.Fatalf("parseCronExpr() error = %v", err)
		}
		got, ok := expr.next(time.Date(2026, time.April, 6, 10, 0, 0, 0, time.UTC))
		if !ok {
			t.Fatal("next() ok = false, want true")
		}
		want := time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("next() = %s, want %s", got, want)
		}
	})

	t.Run("day of month or day of week", func(t *testing.T) {
		expr, err := parseCronExpr("0 9 15 * 1")
		if err != nil {
			t.Fatalf("parseCronExpr() error = %v", err)
		}
		got, ok := expr.next(time.Date(2026, time.April, 13, 9, 0, 0, 0, time.UTC))
		if !ok {
			t.Fatal("next() ok = false, want true")
		}
		want := time.Date(2026, time.April, 15, 9, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("next() = %s, want %s", got, want)
		}
	})
}
