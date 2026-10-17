package sqltext

import (
	"testing"
	"time"
)

func TestDurationStyle_Thresholds(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "text-muted"},
		{1 * time.Millisecond, "text-yellow-500"},
		{5 * time.Millisecond, "text-yellow-500"},
		{10 * time.Millisecond, "text-orange-500"},
		{50 * time.Millisecond, "text-orange-500"},
		{100 * time.Millisecond, "text-red-500"},
		{500 * time.Millisecond, "text-red-500"},
		{1 * time.Second, "text-red-500 font-bold"},
		{5 * time.Second, "text-red-500 font-bold"},
	}
	for _, tc := range cases {
		if got := DurationStyle(tc.d); got != tc.want {
			t.Errorf("DurationStyle(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
