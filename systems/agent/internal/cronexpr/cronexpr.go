// Package cronexpr parses the strict UTC cron expression subset shared by
// q15's background controllers.
package cronexpr

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	cron "github.com/netresearch/go-cron"
)

const searchYears = 8

var parser = cron.MustNewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.DowOrDom,
).WithMaxSearchYears(searchYears)

// Expression is a validated five-field UTC cron expression.
type Expression struct {
	schedule cron.Schedule
}

// Parse validates and parses a strict five-field UTC cron expression.
//
// Macros, embedded timezones, named months/weekdays, seconds, and non-numeric
// extension tokens are intentionally rejected. Callers persist and evaluate
// all schedule timestamps in UTC.
func Parse(raw string) (Expression, error) {
	raw = strings.TrimSpace(raw)
	if err := validateSpec(raw); err != nil {
		return Expression{}, err
	}

	schedule, err := parser.Parse(raw)
	if err != nil {
		return Expression{}, err
	}
	return Expression{schedule: schedule}, nil
}

// Next returns the first scheduled UTC instant strictly after the provided
// time. The bool is false when the expression has no match in the bounded
// search window.
func (e Expression) Next(after time.Time) (time.Time, bool) {
	if e.schedule == nil {
		return time.Time{}, false
	}
	next := e.schedule.Next(after.UTC())
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.UTC(), true
}

// Prev returns the latest scheduled UTC instant strictly before the provided
// time. The bool is false when the expression has no match in the bounded
// search window.
func (e Expression) Prev(before time.Time) (time.Time, bool) {
	schedule, ok := e.schedule.(cron.ScheduleWithPrev)
	if !ok || schedule == nil {
		return time.Time{}, false
	}
	prev := schedule.Prev(before.UTC())
	if prev.IsZero() {
		return time.Time{}, false
	}
	return prev.UTC(), true
}

func validateSpec(raw string) error {
	if raw == "" {
		return fmt.Errorf("cron spec is required")
	}
	if strings.HasPrefix(raw, "@") {
		return fmt.Errorf("cron macros are not supported")
	}
	if strings.HasPrefix(raw, "TZ=") || strings.HasPrefix(raw, "CRON_TZ=") {
		return fmt.Errorf("cron timezones are not supported")
	}

	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return fmt.Errorf("cron spec must contain exactly 5 fields")
	}

	for _, field := range fields {
		if err := validateField(field); err != nil {
			return err
		}
	}
	return nil
}

func validateField(field string) error {
	field = strings.TrimSpace(field)
	if field == "" {
		return fmt.Errorf("field is required")
	}

	for _, r := range field {
		switch {
		case unicode.IsDigit(r):
		case r == '*' || r == ',' || r == '-' || r == '/':
		default:
			return fmt.Errorf("unsupported token %q", string(r))
		}
	}

	for _, item := range strings.Split(field, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return fmt.Errorf("empty list item")
		}
		if err := validateStep(item); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(item string) error {
	if !strings.Contains(item, "/") {
		return nil
	}

	parts := strings.Split(item, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid step expression %q", item)
	}
	base := strings.TrimSpace(parts[0])
	step := strings.TrimSpace(parts[1])
	if base == "" || step == "" {
		return fmt.Errorf("invalid step expression %q", item)
	}
	if base != "*" && !strings.Contains(base, "-") {
		return fmt.Errorf("steps require * or a-b range syntax")
	}
	return nil
}
