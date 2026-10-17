package sqltext

import "time"

// DurationStyle is the tone a statement's duration renders in: muted under
// 1ms, then yellow, orange from 10ms, red from 100ms and bold red from 1s.
// Every SQL trace type styles its durations with it, so a slow statement
// reads the same whichever capture recorded it.
func DurationStyle(d time.Duration) string {
	switch {
	case d >= 1*time.Second:
		return "text-red-500 font-bold"
	case d >= 100*time.Millisecond:
		return "text-red-500"
	case d >= 10*time.Millisecond:
		return "text-orange-500"
	case d >= 1*time.Millisecond:
		return "text-yellow-500"
	default:
		return "text-muted"
	}
}
