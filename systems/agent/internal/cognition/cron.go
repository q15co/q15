package cognition

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	cron "github.com/netresearch/go-cron"
)

const cronSearchYears = 8

var cronParser = cron.MustNewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.DowOrDom,
).WithMaxSearchYears(cronSearchYears)

type cronExpr struct {
	schedule cron.Schedule
}

func parseCronExpr(raw string) (cronExpr, error) {
	raw = strings.TrimSpace(raw)
	if err := validateCronSpec(raw); err != nil {
		return cronExpr{}, err
	}

	schedule, err := cronParser.Parse(raw)
	if err != nil {
		return cronExpr{}, err
	}
	return cronExpr{schedule: schedule}, nil
}

func validateCronSpec(raw string) error {
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
		if err := validateCronField(field); err != nil {
			return err
		}
	}
	return nil
}

func validateCronField(field string) error {
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
		if err := validateStepSyntax(item); err != nil {
			return err
		}
	}
	return nil
}

func validateStepSyntax(item string) error {
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

func (e cronExpr) next(after time.Time) (time.Time, bool) {
	if e.schedule == nil {
		return time.Time{}, false
	}
	next := e.schedule.Next(after.UTC())
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.UTC(), true
}
