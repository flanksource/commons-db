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

	sort.Strings(events)
	causality := wantsCausality(events)

	var b strings.Builder
	fmt.Fprintf(&b, "CREATE EVENT SESSION %s ON SERVER\n", quoteIdent(opts.Name))

	for i, name := range events {
		if i > 0 {
			b.WriteString(",\n")
		}
		writeEventClause(&b, name, opts, causality)
	}

	trackCausality := "OFF"
	if causality {
		trackCausality = "ON"
	}
	fmt.Fprintf(&b, "\nADD TARGET package0.ring_buffer (SET max_memory = %d, max_events_limit = %d)", opts.MaxMemoryKB, opts.MaxEvents)
	fmt.Fprintf(&b, "\nWITH (MAX_DISPATCH_LATENCY = %d SECONDS, TRACK_CAUSALITY = %s, STARTUP_STATE = OFF)", int(RingBufferDispatchLatency/time.Second), trackCausality)
	return b.String(), nil
}

func writeEventClause(b *strings.Builder, name string, opts CreateOptions, causality bool) {
	fmt.Fprintf(b, "ADD EVENT sqlserver.%s (\n", name)

	// Actions: extra columns we want alongside the event's intrinsic fields.
	actions := []string{
		"sqlserver.client_app_name",
		"sqlserver.client_hostname",
		"sqlserver.database_name",
		"sqlserver.session_id",
		"sqlserver.sql_text",
		"sqlserver.username",
	}
	if causality {
		// TRACK_CAUSALITY only makes the activity id available; the action has
		// to be attached to each event for it to reach the ring buffer.
		// Prepended so the ACTION list stays sorted (package0 < sqlserver).
		actions = append([]string{"package0.attach_activity_id"}, actions...)
	}
	fmt.Fprintf(b, "    ACTION (%s)", strings.Join(actions, ", "))

	preds := buildPredicates(name, opts)
	if len(preds) > 0 {
		fmt.Fprintf(b, "\n    WHERE (%s)", strings.Join(preds, " AND "))
	}
	b.WriteString("\n)")
}

func buildPredicates(event string, opts CreateOptions) []string {
	var preds []string
	if opts.DatabaseName != "" {
		preds = append(preds, fmt.Sprintf("sqlserver.database_name = N'%s'", escapeSQLStringLiteral(opts.DatabaseName)))
	}
	if p := buildMatchPredicate("sqlserver.username", opts.Users); p != "" {
		preds = append(preds, p)
	}
	if opts.ExcludeSessionID > 0 {
		preds = append(preds, fmt.Sprintf("sqlserver.session_id <> %d", opts.ExcludeSessionID))
	}
	if p := buildMatchPredicate("sqlserver.client_app_name", opts.Apps); p != "" {
		preds = append(preds, p)
	}
	if p := buildMatchPredicate("sqlserver.client_hostname", opts.Hosts); p != "" {
		preds = append(preds, p)
	}
	if opts.MinDurationMicros > 0 && eventHasDuration(event) {
		preds = append(preds, fmt.Sprintf("duration >= %d", opts.MinDurationMicros))
	}
	return preds
}

func escapeSQLStringLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// buildMatchPredicate translates collections.MatchItem patterns (exact, `*`
// wildcard, `!` exclusion) for one XE field into a CREATE EVENT SESSION WHERE
// fragment, so login filtering executes in SQL Server rather than post-capture
// in Go. Positives are OR-grouped, negatives AND-grouped; an exclusion-only set
// yields just the negatives ("match everything not excluded"), mirroring
// MatchItem's precedence. Returns "" when nothing constrains (no patterns, or
// only `*`). Comma-joined values are flattened the same way EventFilter does, so
// `--user a,b` and `--user a --user b` are equivalent. Matching is
// case-insensitive (like_i_sql_unicode_string), as collections.MatchItems is.
func buildMatchPredicate(field string, patterns []string) string {
	var positives, negatives []string
	for _, raw := range splitPatterns(patterns) {
		p := strings.TrimSpace(raw)
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimSpace(strings.TrimPrefix(p, "!"))
		if p == "" || p == "*" {
			continue
		}
		if neg {
			negatives = append(negatives, matchComparator(field, p, true))
		} else {
			positives = append(positives, matchComparator(field, p, false))
		}
	}
	var groups []string
	if len(positives) > 0 {
		groups = append(groups, "("+strings.Join(positives, " OR ")+")")
	}
	if len(negatives) > 0 {
		groups = append(groups, "("+strings.Join(negatives, " AND ")+")")
	}
	return strings.Join(groups, " AND ")
}

// matchComparator renders one XE comparator for a single MatchItem pattern: an
// exact `field = N'x'` (or `<>` when negated), or a case-insensitive LIKE when
// the pattern carries a `*` wildcard (translated to `%`). Login values do not
// contain LIKE metacharacters, so only the quote is escaped.
func matchComparator(field, pattern string, negate bool) string {
	if strings.Contains(pattern, "*") {
		lit := escapeSQLStringLiteral(strings.ReplaceAll(pattern, "*", "%"))
		like := fmt.Sprintf("sqlserver.like_i_sql_unicode_string(%s, N'%s')", field, lit)
		if negate {
			return "NOT " + like
		}
		return like
	}
	op := "="
	if negate {
		op = "<>"
	}
	return fmt.Sprintf("%s %s N'%s'", field, op, escapeSQLStringLiteral(pattern))
}
