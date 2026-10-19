package xetrace

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/flanksource/sqlglot-go/sqlglot"
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

// eventActions are the extra columns we want alongside each event's intrinsic
// fields. event_sequence is the only one that is unique per event, so it is
// what Event.Key dedups overlapping polls on.
//
// package0.attach_activity_id is deliberately NOT in this list. It is a
// private action (sys.dm_xe_objects.capabilities_desc = 'private'), so naming
// it in an ACTION list fails the whole CREATE with "The event action name,
// "package0.attach_activity_id", is invalid, or the object could not be
// found" — which took out every capture that included sp_statement_completed.
// TRACK_CAUSALITY = ON attaches it to each event on its own, which is what Nest
// reads.
var eventActions = []xeName{
	{"package0", "event_sequence"},
	{"sqlserver", "client_app_name"},
	{"sqlserver", "client_hostname"},
	{"sqlserver", "database_name"},
	{"sqlserver", "session_id"},
	{"sqlserver", "sql_text"},
	{"sqlserver", "username"},
}

// BuildCreateSQL assembles the CREATE EVENT SESSION DDL for the given options.
// Exported for testing.
func BuildCreateSQL(opts CreateOptions) (string, error) {
	events, err := validateCreateOptions(opts)
	if err != nil {
		return "", err
	}
	target, err := buildTarget(opts)
	if err != nil {
		return "", err
	}
	filters, err := sessionFilters(opts)
	if err != nil {
		return "", err
	}
	sort.Strings(events)
	added := make([]ddlNode, 0, len(events))
	for _, name := range events {
		added = append(added, sessionEvent(name, opts.MinDurationMicros, filters))
	}
	return generateTSQL(sqlglot.New("CreateEventSession",
		kv("this", sessionIdent(opts.Name)),
		kv("scope", "SERVER"),
		kv("events", added),
		kv("targets", ddlNodes(target)),
		kv("options", sessionOptions(wantsCausality(events)))))
}

// validateCreateOptions refuses options SQL Server would reject with an opaque
// syntax error, and returns the normalized event list.
func validateCreateOptions(opts CreateOptions) ([]string, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("session name is required")
	}
	events, err := NormalizeEvents(opts.Events)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("at least one event is required")
	}
	if opts.MinDurationMicros < 0 {
		return nil, fmt.Errorf("minimum duration must not be negative")
	}
	// Create turns zero into the configured default, so a value that reaches
	// here is caller-supplied.
	if opts.MaxMemoryKB < 0 {
		return nil, fmt.Errorf("ring buffer max_memory %d KB: must not be negative", opts.MaxMemoryKB)
	}
	if opts.MaxEvents < 0 {
		return nil, fmt.Errorf("ring buffer max_events_limit %d: must not be negative", opts.MaxEvents)
	}
	return events, nil
}

// sessionOptions is the session's WITH list.
func sessionOptions(causality bool) []ddlNode {
	trackCausality := "OFF"
	if causality {
		trackCausality = "ON"
	}
	return ddlNodes(
		option("MAX_DISPATCH_LATENCY", number(int64(DispatchLatency/time.Second)), "SECONDS"),
		option("TRACK_CAUSALITY", word(trackCausality), ""),
		option("STARTUP_STATE", word("OFF"), ""),
	)
}

// buildTarget is the session's single target: the ring buffer by default, or
// an event_file when one is configured. The ring-buffer sizing is refused
// alongside a file target rather than ignored — it would read as a cap on a
// capture that has none.
func buildTarget(opts CreateOptions) (ddlNode, error) {
	if opts.File == nil {
		return eventSessionTarget("ring_buffer",
			option("max_memory", number(opts.MaxMemoryKB), ""),
			option("max_events_limit", number(opts.MaxEvents), "")), nil
	}
	file := *opts.File
	if opts.MaxMemoryKB != 0 || opts.MaxEvents != 0 {
		return nil, fmt.Errorf("event_file target: maxMemoryKb/maxEvents size the ring buffer and do not apply; use maxFileSizeMb/maxRolloverFiles")
	}
	if err := validateEventFilePath(file.Path); err != nil {
		return nil, err
	}
	if file.MaxFileSizeMB < 0 {
		return nil, fmt.Errorf("event_file max_file_size %d MB: must not be negative", file.MaxFileSizeMB)
	}
	if file.MaxRolloverFiles < 0 {
		return nil, fmt.Errorf("event_file max_rollover_files %d: must not be negative", file.MaxRolloverFiles)
	}
	return eventSessionTarget("event_file",
		option("filename", national(file.Path), ""),
		option("max_file_size", number(file.MaxFileSizeMB), ""),
		option("max_rollover_files", number(file.MaxRolloverFiles), "")), nil
}

func eventSessionTarget(name string, set ...ddlNode) ddlNode {
	return sqlglot.New("EventSessionTarget", kv("this", ident(name)), kv("package", ident("package0")), kv("set", set))
}

// sessionEvent is one event's ADD EVENT clause. xml_deadlock_report is added
// bare: a deadlock involves several sessions, so no session-level predicate or
// action describes it, and its report names its own databases.
func sessionEvent(name string, minDurationMicros int64, filters []ddlNode) ddlNode {
	args := []sqlglot.Arg{kv("this", ident(name)), kv("package", ident("sqlserver"))}
	if name == EventXMLDeadlockReport {
		return sqlglot.New("EventSessionEvent", args...)
	}
	if isObjectEvent(name) {
		// database_name is a customizable field of the object events, off by
		// default: it is the object's database, where the database_name action
		// is only the session's current one.
		args = append(args, kv("set", ddlNodes(option("collect_database_name", paren(number(1)), ""))))
	}
	actions := make([]ddlNode, 0, len(eventActions))
	for _, action := range eventActions {
		actions = append(actions, action.column())
	}
	args = append(args, kv("actions", actions))
	if where := joinConditions("And", buildPredicates(name, minDurationMicros, filters)); where != nil {
		args = append(args, kv("where", where))
	}
	return sqlglot.New("EventSessionEvent", args...)
}

// sessionFilters translates the database/user/app/host MultiFilters, and the
// reader's own session, into the predicates every event of the session
// carries, so SQL Server drops an unwanted event before it reaches the target.
func sessionFilters(opts CreateOptions) ([]ddlNode, error) {
	var preds []ddlNode
	for _, f := range []struct {
		label  string
		field  xeName
		values []string
	}{
		{"database", xeName{"sqlserver", "database_name"}, opts.Databases},
		{"user", xeName{"sqlserver", "username"}, opts.Users},
		{"app", xeName{"sqlserver", "client_app_name"}, opts.Apps},
		{"host", xeName{"sqlserver", "client_hostname"}, opts.Hosts},
	} {
		p, err := buildMatchPredicate(f.field, f.values)
		if err != nil {
			return nil, fmt.Errorf("%s filter: %w", f.label, err)
		}
		if p != nil {
			preds = append(preds, p)
		}
	}
	if opts.ExcludeSessionID > 0 {
		preds = append(preds, binary("NEQ", xeName{"sqlserver", "session_id"}.column(), number(opts.ExcludeSessionID)))
	}
	return preds, nil
}

// buildPredicates is one event's WHERE list. The event's intrinsic fields come
// first — duration, or an object event's objectEventPredicates — because they
// are the cheapest tests SQL Server can make and short-circuit the action-based
// filters behind them. Each event gets its own copy of the shared filters: a
// tree node has one parent.
func buildPredicates(event string, minDurationMicros int64, filters []ddlNode) []ddlNode {
	var preds []ddlNode
	switch {
	case minDurationMicros > 0 && eventHasDuration(event):
		preds = append(preds, binary("GTE", eventField("duration"), number(minDurationMicros)))
	case isObjectEvent(event):
		preds = append(preds, objectEventPredicates()...)
	}
	for _, filter := range filters {
		preds = append(preds, filter.Copy())
	}
	return preds
}

// buildStartSQL is the ALTER EVENT SESSION … STATE = START for a session.
func buildStartSQL(session string) (string, error) {
	return generateTSQL(sqlglot.New("AlterEventSession",
		kv("this", sessionIdent(session)), kv("scope", "SERVER"), kv("state", "START")))
}

// buildDropSQL is the DROP EVENT SESSION for a session.
func buildDropSQL(session string) (string, error) {
	return generateTSQL(sqlglot.New("DropEventSession", kv("this", sessionIdent(session)), kv("scope", "SERVER")))
}

// buildReplaceEventSQL is the one batch that swaps an event's predicate: DROP
// EVENT when the session still has it, then ADD EVENT with the new one.
func buildReplaceEventSQL(session, event string, minDurationMicros int64, filters []ddlNode) (string, error) {
	dropped := sqlglot.New("EventSessionEvent",
		kv("this", ident(event)), kv("package", ident("sqlserver")), kv("drop", true))
	ifPresent := sqlglot.New("IfBlock",
		kv("this", sessionHasEvent(session, event)),
		kv("true", alterSessionEvent(session, dropped)))
	added := alterSessionEvent(session, sessionEvent(event, minDurationMicros, filters))
	return generateTSQL(sqlglot.New("Block", kv("expressions", ddlNodes(ifPresent, added))))
}

func alterSessionEvent(session string, event ddlNode) ddlNode {
	return sqlglot.New("AlterEventSession",
		kv("this", sessionIdent(session)), kv("scope", "SERVER"), kv("events", ddlNodes(event)))
}

// sessionHasEvent is EXISTS (SELECT 1 …) over the catalog: whether the server
// session named session still carries event.
func sessionHasEvent(session, event string) ddlNode {
	join := sqlglot.New("Join",
		kv("this", sysTable("server_event_sessions", "s")),
		kv("on", binary("EQ", qualifiedColumn("s", "event_session_id"), qualifiedColumn("e", "event_session_id"))))
	where := binary("And",
		binary("EQ", qualifiedColumn("s", "name"), national(session)),
		binary("EQ", qualifiedColumn("e", "name"), national(event)))
	return sqlglot.New("Exists", kv("this", sqlglot.New("Select",
		kv("expressions", ddlNodes(number(1))),
		kv("from_", sqlglot.New("From", kv("this", sysTable("server_event_session_events", "e")))),
		kv("joins", ddlNodes(join)),
		kv("where", sqlglot.New("Where", kv("this", where))))))
}

func sysTable(name, alias string) ddlNode {
	return sqlglot.New("Table",
		kv("this", ident(name)), kv("db", ident("sys")),
		kv("alias", sqlglot.New("TableAlias", kv("this", ident(alias)))))
}
