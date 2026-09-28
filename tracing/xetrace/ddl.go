package xetrace

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// eventHasDuration reports whether an XE event exposes a `duration` field we
// can filter on.
func eventHasDuration(name string) bool {
	switch name {
	case EventSQLStatementCompleted, EventRPCCompleted, EventSQLBatchCompleted, EventSPStatementCompleted:
		return true
	default:
		return false
	}
}

// wantsCausality reports whether the event set needs TRACK_CAUSALITY. Only
// sp_statement_completed does: its events must be grouped back under the
// rpc_completed/sql_batch_completed that ran them, and SQL Server's activity id
// is the only reliable key for that (inner statements complete BEFORE their
// parent, so timestamp ordering alone cannot reconstruct the nesting).
// Left off otherwise so an ordinary trace's DDL and per-event overhead are
// unchanged.
func wantsCausality(events []string) bool {
	return slices.Contains(events, EventSPStatementCompleted)
}

// BuildCreateSQL assembles the CREATE EVENT SESSION DDL for the given options.
// Exported for testing.
func BuildCreateSQL(opts CreateOptions) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("session name is required")
	}
	events, err := NormalizeEvents(opts.Events)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "", fmt.Errorf("at least one event is required")
	}
	if opts.MinDurationMicros < 0 {
		return "", fmt.Errorf("minimum duration must not be negative")
	}
	// Create turns zero into the configured default, so a value that reaches
	// here is caller-supplied. A negative one would render into the ring_buffer
	// clause and come back as an opaque SQL Server syntax error.
	if opts.MaxMemoryKB < 0 {
		return "", fmt.Errorf("ring buffer max_memory %d KB: must not be negative", opts.MaxMemoryKB)
	}
	if opts.MaxEvents < 0 {
		return "", fmt.Errorf("ring buffer max_events_limit %d: must not be negative", opts.MaxEvents)
	}
	target, err := buildTargetClause(opts)
	if err != nil {
		return "", err
	}

	filters, err := sessionFilters(opts)
	if err != nil {
		return "", err
	}
	sort.Strings(events)
	causality := wantsCausality(events)

	var b strings.Builder
	fmt.Fprintf(&b, "CREATE EVENT SESSION %s ON SERVER\n", quoteIdent(opts.Name))

	for i, name := range events {
		if i > 0 {
			b.WriteString(",\n")
		}
		writeSessionEvent(&b, name, opts.MinDurationMicros, filters)
	}

	trackCausality := "OFF"
	if causality {
		trackCausality = "ON"
	}
	fmt.Fprintf(&b, "\n%s", target)
	fmt.Fprintf(&b, "\nWITH (MAX_DISPATCH_LATENCY = %d SECONDS, TRACK_CAUSALITY = %s, STARTUP_STATE = OFF)", int(DispatchLatency/time.Second), trackCausality)
	return b.String(), nil
}

// buildTargetClause is the session's single ADD TARGET line: the ring buffer by
// default, or an event_file when one is configured. The ring-buffer sizing is
// refused alongside a file target rather than ignored — it would read as a cap
// on a capture that has none.
func buildTargetClause(opts CreateOptions) (string, error) {
	if opts.File == nil {
		return fmt.Sprintf("ADD TARGET package0.ring_buffer (SET max_memory = %d, max_events_limit = %d)", opts.MaxMemoryKB, opts.MaxEvents), nil
	}
	file := *opts.File
	if opts.MaxMemoryKB != 0 || opts.MaxEvents != 0 {
		return "", fmt.Errorf("event_file target: maxMemoryKb/maxEvents size the ring buffer and do not apply; use maxFileSizeMb/maxRolloverFiles")
	}
	if err := validateEventFilePath(file.Path); err != nil {
		return "", err
	}
	if file.MaxFileSizeMB < 0 {
		return "", fmt.Errorf("event_file max_file_size %d MB: must not be negative", file.MaxFileSizeMB)
	}
	if file.MaxRolloverFiles < 0 {
		return "", fmt.Errorf("event_file max_rollover_files %d: must not be negative", file.MaxRolloverFiles)
	}
	return fmt.Sprintf(
		"ADD TARGET package0.event_file (SET filename = N'%s', max_file_size = %d, max_rollover_files = %d)",
		escapeSQLStringLiteral(file.Path), file.MaxFileSizeMB, file.MaxRolloverFiles,
	), nil
}

// writeSessionEvent writes one event's ADD EVENT clause. xml_deadlock_report
// is added bare: a deadlock involves several sessions, so no session-level
// predicate or action describes it, and its report names its own databases.
func writeSessionEvent(b *strings.Builder, name string, minDurationMicros int64, filters []string) {
	if name == EventXMLDeadlockReport {
		b.WriteString("ADD EVENT sqlserver.xml_deadlock_report")
		return
	}
	writeEventClause(b, name, buildPredicates(name, minDurationMicros, filters))
}

func writeEventClause(b *strings.Builder, name string, preds []string) {
	fmt.Fprintf(b, "ADD EVENT sqlserver.%s (\n", name)
	if isObjectEvent(name) {
		// database_name is a customizable field of the object events, off by
		// default: it is the object's database, where the database_name action
		// is only the session's current one.
		b.WriteString("    SET collect_database_name = (1)\n")
	}

	// Actions: extra columns we want alongside the event's intrinsic fields.
	// event_sequence is the only one that is unique per event, so it is what
	// Event.Key dedups overlapping polls on.
	actions := []string{
		"package0.event_sequence",
		"sqlserver.client_app_name",
		"sqlserver.client_hostname",
		"sqlserver.database_name",
		"sqlserver.session_id",
		"sqlserver.sql_text",
		"sqlserver.username",
	}
	// package0.attach_activity_id is deliberately NOT in this list. It is a
	// private action (sys.dm_xe_objects.capabilities_desc = 'private'), so
	// naming it in an ACTION list fails the whole CREATE with "The event action
	// name, "package0.attach_activity_id", is invalid, or the object could not
	// be found" — which took out every capture that included
	// sp_statement_completed. TRACK_CAUSALITY = ON attaches it to each event on
	// its own, which is what Nest reads.
	fmt.Fprintf(b, "    ACTION (%s)", strings.Join(actions, ", "))

	if len(preds) > 0 {
		fmt.Fprintf(b, "\n    WHERE (%s)", strings.Join(preds, " AND "))
	}
	b.WriteString("\n)")
}

// sessionFilters translates the database/user/app/host MultiFilters, and the
// reader's own session, into the predicates every event of the session
// carries, so SQL Server drops an unwanted event before it reaches the target.
func sessionFilters(opts CreateOptions) ([]string, error) {
	var preds []string
	for _, f := range []struct {
		label, field string
		values       []string
	}{
		{"database", "sqlserver.database_name", opts.Databases},
		{"user", "sqlserver.username", opts.Users},
		{"app", "sqlserver.client_app_name", opts.Apps},
		{"host", "sqlserver.client_hostname", opts.Hosts},
	} {
		p, err := buildMatchPredicate(f.field, f.values)
		if err != nil {
			return nil, fmt.Errorf("%s filter: %w", f.label, err)
		}
		if p != "" {
			preds = append(preds, p)
		}
	}
	if opts.ExcludeSessionID > 0 {
		preds = append(preds, fmt.Sprintf("sqlserver.session_id <> %d", opts.ExcludeSessionID))
	}
	return preds, nil
}

// buildPredicates is one event's WHERE list. The event's intrinsic fields come
// first — duration, or an object event's objectEventPredicates — because they
// are the cheapest tests SQL Server can make and short-circuit the action-based
// filters behind them.
func buildPredicates(event string, minDurationMicros int64, filters []string) []string {
	var preds []string
	switch {
	case minDurationMicros > 0 && eventHasDuration(event):
		preds = append(preds, fmt.Sprintf("duration >= %d", minDurationMicros))
	case isObjectEvent(event):
		preds = append(preds, objectEventPredicates...)
	}
	return append(preds, filters...)
}

func escapeSQLStringLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
