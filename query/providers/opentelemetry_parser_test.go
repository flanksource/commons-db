package providers

import (
	"strconv"
	"testing"
	"time"
)

func TestTraceDurationMillisUsesFieldUnits(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		field  string
		format string
		want   float64
	}{
		{name: "jaeger microseconds", value: int64(123000), field: "duration", format: "jaeger", want: 123},
		{name: "flat nanoseconds", value: int64(500000), field: "duration", format: "flat", want: 0.5},
		{name: "explicit milliseconds", value: int64(500), field: "duration_ms", format: "flat", want: 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceDurationMillis(tt.value, tt.field, tt.format); got != tt.want {
				t.Fatalf("traceDurationMillis() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every epoch unit must round-trip to the same instant. Microseconds are the
// regression: without their own magnitude band they were read as nanoseconds
// and normalized to 1970.
func TestNormalizeTraceTimestampDetectsEpochUnit(t *testing.T) {
	instant := time.Date(2026, time.July, 27, 8, 42, 43, 0, time.UTC)
	want := instant.Format(time.RFC3339Nano)

	tests := []struct {
		name string
		raw  int64
	}{
		{name: "seconds", raw: instant.Unix()},
		{name: "milliseconds", raw: instant.UnixMilli()},
		{name: "microseconds", raw: instant.UnixMicro()},
		{name: "nanoseconds", raw: instant.UnixNano()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTraceTimestamp(strconv.FormatInt(tt.raw, 10))
			parsed, err := time.Parse(time.RFC3339Nano, got)
			if err != nil {
				t.Fatalf("normalizeTraceTimestamp(%d) = %q, not a timestamp: %v", tt.raw, got, err)
			}
			if !parsed.UTC().Equal(instant) {
				t.Fatalf("normalizeTraceTimestamp(%d) = %q, want %q", tt.raw, parsed.UTC().Format(time.RFC3339Nano), want)
			}
		})
	}
}

func TestNormalizeTraceTimestampPassesThroughNonEpochValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "rfc3339", raw: "2026-07-27T08:42:43Z", want: "2026-07-27T08:42:43Z"},
		{name: "unparseable", raw: "not-a-timestamp", want: "not-a-timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeTraceTimestamp(tt.raw); got != tt.want {
				t.Fatalf("normalizeTraceTimestamp(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
