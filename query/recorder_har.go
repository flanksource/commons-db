package query

import (
	"time"

	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons/logger"
)

// recordLine files one of this operation's log records into the request's
// devtools record, tagged with the operation it came from — which is what makes
// two SQL statements in one request tellable apart.
func (operation *connectionOperation) recordLine(level logger.LogLevel, event observability.Event, values map[string]any) {
	if operation.record == nil {
		return
	}
	operation.record.Log(LogLine{
		Level:   level.String(),
		Event:   string(event),
		Message: operation.provider + " " + string(event),
		Values:  SanitizeDiagnosticValues(values),
	})
}

// publish hands one finished operation to the request's devtools record.
//
// It reads the operation's own collector rather than installing the recorder as
// the context's parent collector. Two reasons, and both are load-bearing:
//
//   - prepareConnectionOperation already forwards every entry to whatever parent
//     the context carries, so a recorder installed there would receive each entry
//     through the forward handler *and* be read here — every request duplicated.
//   - the parent slot is not ours to take. A CLI --har export installs one, and
//     overwriting it would silently produce an empty export.
//
// Nothing shared is mutated. Entries() copies under the collector's own mutex,
// DroppedEntries() reads under it, and Config is only ever read — the earlier
// data race came from assigning to Config on a shared collector, which this
// package has never done.
func (operation *connectionOperation) publish(duration time.Duration, rows int, runErr error) {
	if operation.record == nil {
		return
	}
	result := OperationResult{
		Diagnostics: operation.diagnostics.Snapshot(),
		Duration:    duration,
		Rows:        rows,
		Err:         runErr,
	}
	if operation.collector != nil {
		result.Entries = operation.collector.Entries()
		result.Dropped = operation.collector.DroppedEntries()
		result.Sensitive = operation.collector.Config.CaptureSensitive
	}
	operation.record.Complete(result)
}
