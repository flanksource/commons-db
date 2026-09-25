package sqltrace

import (
	"time"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// ev builds a captured event with the fields the registry's tests key off:
// enough to be distinguishable, and nothing that matters only to a renderer.
func ev(sid int, stmt string, at time.Time) xetrace.Event {
	return xetrace.Event{SessionID: sid, Duration: time.Millisecond, Statement: stmt, SQL: stmt, Timestamp: at}
}

func statements(events []xetrace.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Statement)
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
