package providers

import "testing"

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
