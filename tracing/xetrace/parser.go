package xetrace

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// spExecuteMarker is the display form used when an sp_execute names a prepared
// handle whose sp_prepexec happened before capture began. The values are known,
// the SQL text is not — say so rather than render a bare handle id as if it
// were the statement.
const spExecuteMarker = "unresolved prepared handle"

// Event is a decoded row from a ring_buffer target.
type Event struct {
	Name          string        `json:"name"`
	Timestamp     time.Time     `json:"timestamp"`
	Duration      time.Duration `json:"duration"`
	CPUTime       time.Duration `json:"cpu_time"`
	LogicalReads  int64         `json:"logical_reads"`
	PhysicalReads int64         `json:"physical_reads"`
	Writes        int64         `json:"writes"`
	RowCount      int64         `json:"row_count"`
	DatabaseName  string        `json:"database_name"`
	ClientApp     string        `json:"client_app_name"`
	ClientHost    string        `json:"client_hostname"`
	Username      string        `json:"username"`
	SessionID     int           `json:"session_id"`
	Statement     string        `json:"raw_statement,omitempty"`
	SQL           string        `json:"statement"`
	StatementType StatementType `json:"statement_type,omitempty"`
	Tables        []string      `json:"tables,omitempty"`
	ErrorNumber   int           `json:"error_number,omitempty"`
	ErrorMessage  string        `json:"error_message,omitempty"`

	// ObjectName is the procedure an event ran inside, captured whenever the
	// event exposes it — rpc_completed always does; sp_statement_completed
	// reports object_id instead on some server versions, which is kept in
	// ObjectID. Neither is the attribution mechanism for inner statements:
	// that is Nest, keyed on ActivityID. These are for display only.
	ObjectName string `json:"object_name,omitempty"`
	ObjectID   int64  `json:"object_id,omitempty"`

	// ActivityID and ActivitySeq come from package0.attach_activity_id, which
	// is only attached when TRACK_CAUSALITY is on (see wantsCausality). Every
	// event raised while servicing one request shares an ActivityID, with
	// ActivitySeq increasing in completion order — this is what lets Nest put
	// a procedure's inner statements under the call that ran them.
	ActivityID  string `json:"activity_id,omitempty"`
	ActivitySeq int    `json:"activity_seq,omitempty"`

	// Children are the inner statements attributed to this event by Nest.
	// Always empty until Nest runs.
	Children []Event `json:"children,omitempty"`

	// ParamsUnavailable marks a statement whose parameter values are not
	// recoverable from the trace — a positional `{call p(?,?)}` that the
	// driver did not expand, or an sp_execute whose prepared handle was
	// established before capture started. The placeholders are shown as
	// captured; this flag stops them being read as the literal call.
	ParamsUnavailable bool `json:"params_unavailable,omitempty"`
}

// MergedStatement returns the SQL with parameters inlined. For RPC events
// (sp_prepexec, sp_executesql) this unwraps the scaffold and substitutes
// @P0/@P1/… with their literal values. For plain statements it returns
// the whitespace-collapsed original.
func (e Event) MergedStatement() string {
	stmt := collapseWhitespace(strings.TrimSpace(e.Statement))
	if unwrapped, ok := UnwrapRPC(stmt); ok {
		return unwrapped
	}
	return stmt
}

// Key returns a string uniquely identifying this event within a ring buffer.
// Used by callers to deduplicate across overlapping polls.
func (e Event) Key() string {
	return fmt.Sprintf("%s|%s|%d|%d|%s|%d|%s", e.Name, e.Timestamp.Format(time.RFC3339Nano), e.SessionID, int64(e.Duration), e.ActivityID, e.ActivitySeq, e.Statement)
}

// RingBufferStats are the bookkeeping attributes SQL Server puts on the
// <RingBufferTarget> root. They describe the target itself rather than any
// event in it, and they are the only way to tell that the server produced more
// events than we read back:
//
//   - TotalEventsProcessed counts every event the session has ever dispatched
//     to the target, so the growth between two polls minus the events we
//     actually delivered is the number the ring buffer evicted unseen;
//   - DroppedCount is the server's own count of events it refused to buffer;
//   - Truncated reports that the DMV cut target_data short, which silently
//     shortens (or corrupts) the document we parse.
//
// Without these a skipped or failed poll loses events with no trace at all.
type RingBufferStats struct {
	Truncated            bool  `json:"truncated,omitempty"`
	ProcessingTime       int64 `json:"processing_time,omitempty"`
	TotalEventsProcessed int64 `json:"total_events_processed,omitempty"`
	EventCount           int64 `json:"event_count"`
	DroppedCount         int64 `json:"dropped_count,omitempty"`
	MemoryUsed           int64 `json:"memory_used,omitempty"`
}

// RingBufferSnapshot is one read of a ring_buffer target: the events it held
// and the target's own bookkeeping at that moment.
type RingBufferSnapshot struct {
	Events []Event
	// ExcludedKeys are the Event.Keys of events this read DID see but chose not
	// to deliver — driver chatter dropped by ParseRingBuffer, and events removed
	// by the caller's Filter. They exist purely so Drain can tell "we skipped
	// this" apart from "the buffer evicted this before we got to it".
	//
	// Without them the drop metric subtracts a post-filter delivered count from
	// the server's pre-filter totalEventsProcessed, so every deliberately
	// skipped event is reported as lost. That was not hypothetical: on a real
	// intake run it reported ~560 events lost per run, and disabling the noise
	// drop took the same run to zero. The warning was measuring its own filter.
	ExcludedKeys []string
	Stats        RingBufferStats
}

type ringBufferTarget struct {
	XMLName xml.Name `xml:"RingBufferTarget"`
	// SQL Server writes truncated as 0/1, which xml cannot decode into a bool.
	Truncated            int           `xml:"truncated,attr"`
	ProcessingTime       int64         `xml:"processingTime,attr"`
	TotalEventsProcessed int64         `xml:"totalEventsProcessed,attr"`
	EventCount           int64         `xml:"eventCount,attr"`
	DroppedCount         int64         `xml:"droppedCount,attr"`
	MemoryUsed           int64         `xml:"memoryUsed,attr"`
	Events               []rawXMLEvent `xml:"event"`
}

func (rb ringBufferTarget) stats() RingBufferStats {
	return RingBufferStats{
		Truncated:            rb.Truncated != 0,
		ProcessingTime:       rb.ProcessingTime,
		TotalEventsProcessed: rb.TotalEventsProcessed,
		EventCount:           rb.EventCount,
		DroppedCount:         rb.DroppedCount,
		MemoryUsed:           rb.MemoryUsed,
	}
}

type rawXMLEvent struct {
	Name      string        `xml:"name,attr"`
	Timestamp string        `xml:"timestamp,attr"`
	Data      []rawXMLField `xml:"data"`
	Actions   []rawXMLField `xml:"action"`
}

type rawXMLField struct {
	Name  string `xml:"name,attr"`
	Type  string `xml:"type,attr"`
	Value string `xml:"value"`
	Text  string `xml:"text"`
}

// ParseRingBuffer decodes a ring_buffer target_data payload into the events it
// holds plus the target's own bookkeeping. Exported so it can be unit-tested
// against captured fixtures without a DB.
func ParseRingBuffer(payload string) (RingBufferSnapshot, error) {
	var rb ringBufferTarget
	if err := xml.Unmarshal([]byte(payload), &rb); err != nil {
		return RingBufferSnapshot{}, fmt.Errorf("decode ring_buffer xml: %w", err)
	}
	out := make([]Event, 0, len(rb.Events))
	var excluded []string
	for _, raw := range rb.Events {
		e := toEvent(raw)
		if isNoiseStatement(e.Statement) {
			// Read, but deliberately not delivered. The key still has to be
			// reported: Drain measures eviction as "dispatched minus observed",
			// and an event we dropped on purpose is observed, not lost.
			excluded = append(excluded, e.Key())
			continue
		}
		out = append(out, e)
	}
	return RingBufferSnapshot{Events: out, ExcludedKeys: excluded, Stats: rb.stats()}, nil
}

func toEvent(raw rawXMLEvent) Event {
	e := Event{Name: raw.Name}
	if t, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
		e.Timestamp = t
	} else if t, err := time.Parse("2006-01-02T15:04:05.999Z", raw.Timestamp); err == nil {
		e.Timestamp = t
	}

	for _, f := range raw.Data {
		applyField(&e, f)
	}
	for _, f := range raw.Actions {
		applyField(&e, f)
	}
	deriveFromStatement(&e)
	return e
}

// deriveFromStatement (re)computes every field derived from the raw captured
// statement: the display SQL with RPC parameters inlined, its classification,
// its referenced tables/objects, and whether parameters are missing.
//
// It must be re-run whenever e.SQL changes after parsing — HandleCache.Resolve
// rewrites an sp_execute into its real call, and leaving the stale
// classification behind would keep the event invisible to --type and --table.
func deriveFromStatement(e *Event) {
	e.SQL = e.MergedStatement()
	deriveFromSQL(e)
}

// deriveFromSQL refreshes the fields computed from e.SQL alone, without
// re-deriving e.SQL itself from the raw statement.
func deriveFromSQL(e *Event) {
	e.StatementType = classifyStatement(e.SQL)
	e.Tables = extractTables(e.SQL)
	e.ParamsUnavailable = hasUnboundParams(e.SQL)
}

func applyField(e *Event, f rawXMLField) {
	val := f.Value
	if val == "" {
		val = f.Text
	}
	switch f.Name {
	case "duration":
		e.Duration = time.Duration(parseInt64(val)) * time.Microsecond
	case "cpu_time":
		e.CPUTime = time.Duration(parseInt64(val)) * time.Microsecond
	case "logical_reads":
		e.LogicalReads = parseInt64(val)
	case "physical_reads":
		e.PhysicalReads = parseInt64(val)
	case "writes":
		e.Writes = parseInt64(val)
	case "row_count":
		e.RowCount = parseInt64(val)
	case "statement", "batch_text", "sql_text":
		if e.Statement == "" {
			e.Statement = val
		}
	case "database_name":
		e.DatabaseName = val
	case "client_app_name":
		e.ClientApp = val
	case "client_hostname":
		e.ClientHost = val
	case "username":
		e.Username = val
	case "session_id":
		e.SessionID, _ = strconv.Atoi(val)
	case "error_number":
		e.ErrorNumber, _ = strconv.Atoi(val)
	case "message":
		e.ErrorMessage = val
	case "object_name":
		e.ObjectName = val
	case "object_id":
		e.ObjectID = parseInt64(val)
	case "attach_activity_id":
		e.ActivityID, e.ActivitySeq = parseActivityID(val)
	}
}

// parseActivityID splits an attach_activity_id value into its task GUID and
// sequence number. SQL Server renders it as "<guid>-<seq>", and the GUID itself
// contains hyphens, so the split is on the LAST one. A value without a parseable
// trailing sequence still yields the id, so grouping survives an unexpected
// format even if ordering degrades.
func parseActivityID(val string) (string, int) {
	i := strings.LastIndex(val, "-")
	if i < 0 {
		return val, 0
	}
	seq, err := strconv.Atoi(val[i+1:])
	if err != nil {
		return val, 0
	}
	return val[:i], seq
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// noiseStatements is the canonicalized (upper-case, whitespace-collapsed,
// trailing-semicolon-stripped) set of SQL Server driver chatter that
// ParseRingBuffer drops. Extend this set — not a substring matcher — when
// new pure-noise statements appear in traces.
var noiseStatements = map[string]struct{}{
	"IF @@TRANCOUNT > 0":             {},
	"IF @@TRANCOUNT > 0 COMMIT TRAN": {},
	"COMMIT TRAN":                    {},
	"SELECT 1":                       {},
}

// noisePrefixes are canonicalized statement prefixes that are always noise
// regardless of trailing arguments. Used for driver chatter whose payload
// varies (e.g. sp_unprepare takes a handle id, SET TEXTSIZE takes a
// byte count, SET QUOTED_IDENTIFIER takes ON/OFF).
var noisePrefixes = []string{
	"EXEC SP_UNPREPARE ",
	"SET QUOTED_IDENTIFIER ",
	"SET TEXTSIZE ",
	"SET ARITHABORT ",
	"SET NUMERIC_ROUNDABORT ",
	"SET ANSI_NULLS ",
	"SET ANSI_NULL_DFLT_ON ",
	"SET ANSI_PADDING ",
	"SET ANSI_WARNINGS ",
	"SET CONCAT_NULL_YIELDS_NULL ",
	"SET CURSOR_CLOSE_ON_COMMIT ",
	"SET IMPLICIT_TRANSACTIONS ",
	"SET LOCK_TIMEOUT ",
	"SET DATEFORMAT ",
	"SET DATEFIRST ",
	"SET LANGUAGE ",
	"SET NOCOUNT ",
	"SET TRANSACTION ISOLATION LEVEL ",
	"SET XACT_ABORT ",
	"SET DEADLOCK_PRIORITY ",
	"SET ROWCOUNT ",
	"SET FMTONLY ",
	"SET NO_BROWSETABLE ",
}

// isNoiseStatement reports whether stmt is a known driver chatter statement
// that should be dropped from trace output. Exact-match only after
// normalization — real queries that happen to contain these tokens as
// substrings (e.g. "SELECT 1 FROM dual") are preserved.
func isNoiseStatement(stmt string) bool {
	s := collapseWhitespace(strings.TrimSpace(stmt))
	s = strings.TrimSuffix(s, ";")
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	upper := strings.ToUpper(s)
	if _, ok := noiseStatements[upper]; ok {
		return true
	}
	for _, p := range noisePrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}
