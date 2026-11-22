package sessionstoretest

import (
	"time"

	"github.com/flanksource/commons-db/query"
)

// Epoch anchors the contract fixture an hour back, well inside every
// store's retention window, so window filters read as minute offsets.
func Epoch() time.Time { return time.Now().Add(-time.Hour).Truncate(time.Millisecond).UTC() }

func Record(epoch time.Time, id, profile string, state query.SessionState, startedMin int, events int64, labels map[string]string) query.SessionRecord {
	started := epoch.Add(time.Duration(startedMin) * time.Minute)
	return query.SessionRecord{
		SessionStart: query.SessionStart{
			SchemaVersion: query.SessionSchemaVersion, ID: id, Profile: profile, Kind: query.KindCapture,
			Role: query.SessionRoleCapture, Labels: labels, Principal: "admin",
			Params: map[string]any{"class": "com.example.Listener"},
			Owner:  query.SessionOwner{Host: "pod-a", PID: 1, Boot: "boot-a"}, StartedAt: started,
		},
		SessionStatus: query.SessionStatus{State: state, EventCount: events, UpdatedAt: started, HeartbeatAt: started},
	}
}

// Fixture mirrors the query package's filter fixture: b and c start at
// the same minute so a startedAt sort needs the id tie-breaker, and d has no
// labels at all.
func Fixture(epoch time.Time) []query.SessionRecord {
	restarted := Record(epoch, "f", "trace-capture/jvm_trace", query.SessionRunning, 5, 1, map[string]string{"target": "cycle", "origin": "web"})
	restarted.RestartOf = "c"
	restarted.Principal = "operator"
	return []query.SessionRecord{
		Record(epoch, "c", "trace-capture/jvm_trace", query.SessionStopped, 2, 30, map[string]string{"target": "cycle", "origin": "web"}),
		Record(epoch, "a", "trace-capture/jvm_trace", query.SessionRunning, 0, 10, map[string]string{"target": "oipa", "origin": "testplan"}),
		Record(epoch, "b", "trace-capture/sql_xevent", query.SessionCompleted, 2, 20, map[string]string{"target": "cycle", "origin": "web"}),
		Record(epoch, "d", "trace-capture/jvm_trace", query.SessionFailed, 3, 5, nil),
		restarted,
	}
}
