// Package datetime parses the date values accepted by query parameters.
package datetime

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/gomplate/v3/funcs"
	"github.com/timberio/go-datemath"
)

// Value is a parsed instant plus the syntax that affects range semantics.
type Value struct {
	Time     time.Time
	DateOnly bool
	DateMath bool
}

// Parse accepts OpenSearch date math and the absolute formats supported by
// query date parameters. now is injected so one paged execution has one range.
func Parse(raw string, now time.Time) (Value, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Value{}, fmt.Errorf("date is empty")
	}
	if IsDateMath(value) {
		if now.IsZero() {
			now = time.Now()
		}
		parsed, err := datemath.ParseAndEvaluate(value, datemath.WithNow(now))
		if err != nil {
			return Value{}, fmt.Errorf("invalid date math %q: %w", value, err)
		}
		return Value{Time: parsed, DateMath: true}, nil
	}
	parsed := funcs.ParseDateTime(value)
	if parsed == nil {
		return Value{}, fmt.Errorf("value %q is not a valid date or date math expression", value)
	}
	_, dateOnlyErr := time.Parse(time.DateOnly, value)
	return Value{Time: *parsed, DateOnly: dateOnlyErr == nil}, nil
}

// IsDateMath reports syntax OpenSearch evaluates relative to its request time.
func IsDateMath(value string) bool {
	return strings.HasPrefix(value, "now") || strings.Contains(value, "||")
}
