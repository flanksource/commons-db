package xetrace

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"reflect"
	"regexp"
	"slices"
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
	Name              string         `json:"name"`
	Timestamp         time.Time      `json:"timestamp"`
	Duration          time.Duration  `json:"duration"`
	CPUTime           time.Duration  `json:"cpu_time"`
	LogicalReads      int64          `json:"logical_reads"`
	PhysicalReads     int64          `json:"physical_reads"`
	Writes            int64          `json:"writes"`
	RowCount          int64          `json:"row_count"`
	DatabaseName      string         `json:"database_name"`
	ClientApp         string         `json:"client_app_name"`
	ClientHost        string         `json:"client_hostname"`
	Username          string         `json:"username"`
	SessionID         int            `json:"session_id"`
	Statement         string         `json:"raw_statement,omitempty"`
	SQL               string         `json:"statement"`
	StatementType     StatementType  `json:"statement_type,omitempty"`
	Tables            []string       `json:"tables,omitempty"`
	ErrorNumber       int            `json:"error_number,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	AdditionalFields  map[string]any `json:"additional_fields,omitempty"`
	DeadlockReportXML string         `json:"deadlock_report_xml,omitempty"`
	DeadlockDatabases []string       `json:"deadlock_databases,omitempty"`

	// ObjectName is the procedure an event ran inside, captured whenever the
	// event exposes it — rpc_completed always does; sp_statement_completed
	// reports object_id instead on some server versions, which is kept in
	// ObjectID. Neither is the attribution mechanism for inner statements:
	// that is Nest, keyed on ActivityID. These are for display only.
	ObjectName string `json:"object_name,omitempty"`
	ObjectID   int64  `json:"object_id,omitempty"`

	// ObjectType is what an object event created, altered or deleted, as SQL
	// Server's object_type map names it: USRTAB, INDEX, PROC, VIEW, …. Empty
	// for every other event.
	ObjectType string `json:"object_type,omitempty"`

	// ActivityID and ActivitySeq come from package0.attach_activity_id, which
	// is only attached when TRACK_CAUSALITY is on (see wantsCausality). Every
	// event raised while servicing one request shares an ActivityID, with
	// ActivitySeq increasing in completion order — this is what lets Nest put
	// a procedure's inner statements under the call that ran them.
	ActivityID  string `json:"activity_id,omitempty"`
	ActivitySeq int    `json:"activity_seq,omitempty"`

	// Sequence is package0.event_sequence: the session's running number for this
	// event, unique within one capture. It is what keys an event, because two
	// distinct events can agree on every other field — a connection reset raises
	// 5701 and 5703 in the same millisecond on one session, with no duration and
	// no statement.
	Sequence int64 `json:"sequence,omitempty"`

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
// Used by callers to deduplicate across overlapping polls. Sequence is what
// makes it unique; the other fields only describe the event, and without the
// sequence two events that agree on all of them collapse into one.
func (e Event) Key() string {
	if e.Sequence == 0 {
		fields, _ := json.Marshal(e.AdditionalFields)
		payload := fmt.Sprintf("%s|%s|%d|%d|%s|%s|%s", e.Name, e.Timestamp.Format(time.RFC3339Nano), e.SessionID, e.Duration, e.Statement, fields, e.DeadlockReportXML)
		return fmt.Sprintf("raw|%x", sha256.Sum256([]byte(payload)))
	}
	return fmt.Sprintf("%d|%s|%d|%d|%s", e.Sequence, e.Timestamp.Format(time.RFC3339Nano), e.SessionID, int64(e.Duration), e.Statement)
}

// TargetStats is a poll's bookkeeping about the target itself rather than any
// event in it, and it is the only way to tell that the server produced more
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
//
// A ring_buffer target fills all of them from the <RingBufferTarget> root. An
// event_file target has no such counters: only DroppedCount is available (from
// sys.dm_xe_sessions), and TotalEventsProcessed stays zero, which switches off
// Drain's eviction-delta arm — correctly, because a file target does not evict.
type TargetStats struct {
	Truncated            bool  `json:"truncated,omitempty"`
	ProcessingTime       int64 `json:"processing_time,omitempty"`
	TotalEventsProcessed int64 `json:"total_events_processed,omitempty"`
	EventCount           int64 `json:"event_count"`
	DroppedCount         int64 `json:"dropped_count,omitempty"`
	MemoryUsed           int64 `json:"memory_used,omitempty"`
}

// TargetSnapshot is one read of a session's target — the ring buffer's current
// contents, or the .xel rows an event_file target has flushed since the
// previous poll — plus the target's own bookkeeping at that moment.
type TargetSnapshot struct {
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
	Stats        TargetStats
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

func (rb ringBufferTarget) stats() TargetStats {
	return TargetStats{
		Truncated:            rb.Truncated != 0,
		ProcessingTime:       rb.ProcessingTime,
		TotalEventsProcessed: rb.TotalEventsProcessed,
		EventCount:           rb.EventCount,
		DroppedCount:         rb.DroppedCount,
		MemoryUsed:           rb.MemoryUsed,
	}
}

type rawXMLEvent struct {
	Name              string        `xml:"name,attr"`
	Timestamp         string        `xml:"timestamp,attr"`
	Data              []rawXMLField `xml:"data"`
	Actions           []rawXMLField `xml:"action"`
	DeadlockReportXML string        `xml:"-"`
	DeadlockDatabases []string      `xml:"-"`
}

type rawXMLField struct {
	Name  string `xml:"name,attr"`
	Type  string `xml:"type,attr"`
	Value struct {
		Text  string `xml:",chardata"`
		Inner string `xml:",innerxml"`
	} `xml:"value"`
	Text string `xml:"text"`
}

func (f rawXMLField) scalar() string {
	if f.Value.Text != "" {
		return f.Value.Text
	}
	return f.Text
}

// ParseRingBuffer decodes a ring_buffer target_data payload into the events it
// holds plus the target's own bookkeeping. Exported so it can be unit-tested
// against captured fixtures without a DB.
func ParseRingBuffer(payload string) (TargetSnapshot, error) {
	var rb ringBufferTarget
	if err := xml.Unmarshal([]byte(payload), &rb); err != nil {
		return TargetSnapshot{}, fmt.Errorf("decode ring_buffer xml: %w", err)
	}
	for i := range rb.Events {
		if err := validateAdditionalFields(&rb.Events[i]); err != nil {
			return TargetSnapshot{}, err
		}
	}
	events, excluded := collectEvents(rb.Events)
	return TargetSnapshot{Events: events, ExcludedKeys: excluded, Stats: rb.stats()}, nil
}

// collectEvents decodes raw XE events and splits them into the ones to deliver
// and the keys of the driver chatter dropped along the way. Shared by both
// targets so a ring_buffer poll and an event_file poll drop exactly the same
// noise and account for it the same way.
func collectEvents(raws []rawXMLEvent) ([]Event, []string) {
	out := make([]Event, 0, len(raws))
	var excluded []string
	for _, raw := range raws {
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
	return out, excluded
}

// parseEventDataRow decodes one sys.fn_xe_file_target_read_file row's
// event_data column, which holds a single <event> document rather than the
// ring buffer's whole target payload.
func parseEventDataRow(payload string) (rawXMLEvent, error) {
	var raw rawXMLEvent
	if err := xml.Unmarshal([]byte(payload), &raw); err != nil {
		return rawXMLEvent{}, fmt.Errorf("decode event_file event xml: %w", err)
	}
	if err := validateAdditionalFields(&raw); err != nil {
		return rawXMLEvent{}, err
	}
	return raw, nil
}

func validateAdditionalFields(event *rawXMLEvent) error {
	seen := map[string]any{}
	for _, field := range append(append([]rawXMLField{}, event.Data...), event.Actions...) {
		if event.Name == EventXMLDeadlockReport && field.Name == "xml_report" {
			databases, err := deadlockDatabases(field.Value.Inner)
			if err != nil {
				return fmt.Errorf("decode xml_deadlock_report: %w", err)
			}
			event.DeadlockReportXML = field.Value.Inner
			event.DeadlockDatabases = databases
			continue
		}
		if isPromotedField(field.Name) {
			continue
		}
		value := field.scalar()
		parsed := additionalValue(field.Type, value)
		if prior, ok := seen[field.Name]; ok && !reflect.DeepEqual(prior, parsed) {
			return fmt.Errorf("decode event %q: additional field %q has conflicting values", event.Name, field.Name)
		}
		seen[field.Name] = parsed
	}
	if event.Name == EventXMLDeadlockReport && !slices.ContainsFunc(event.Data, func(field rawXMLField) bool { return field.Name == "xml_report" }) {
		return fmt.Errorf("decode xml_deadlock_report: missing xml_report data")
	}
	return nil
}

func deadlockDatabases(report string) ([]string, error) {
	var graph struct {
		XMLName   xml.Name `xml:"deadlock"`
		Processes []struct {
			Database string `xml:"currentdbname,attr"`
		} `xml:"process-list>process"`
	}
	if err := xml.Unmarshal([]byte(report), &graph); err != nil {
		return nil, err
	}
	if len(graph.Processes) == 0 {
		return nil, fmt.Errorf("deadlock report lists no processes")
	}
	var databases []string
	for _, process := range graph.Processes {
		if process.Database != "" && !slices.Contains(databases, process.Database) {
			databases = append(databases, process.Database)
		}
	}
	return databases, nil
}

func isPromotedField(name string) bool {
	switch name {
	case "duration", "cpu_time", "logical_reads", "physical_reads", "writes", "row_count",
		"statement", "batch_text", "sql_text", "database_name", "client_app_name", "client_hostname",
		"username", "session_id", "error_number", "message", "object_name", "object_id", "object_type",
		"attach_activity_id", "event_sequence", "xml_report":
		return true
	default:
		return false
	}
}

func toEvent(raw rawXMLEvent) Event {
	e := Event{Name: raw.Name, DeadlockReportXML: raw.DeadlockReportXML, DeadlockDatabases: raw.DeadlockDatabases}
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
// re-deriving e.SQL itself from the raw statement. An object event is DDL
// whatever its batch opens with: the event itself reports a committed schema
// change.
func deriveFromSQL(e *Event) {
	e.StatementType = classifyStatement(e.SQL)
	if isObjectEvent(e.Name) {
		e.StatementType = StmtDDL
	}
	e.Tables = extractTables(e.SQL)
	e.ParamsUnavailable = hasUnboundParams(e.SQL)
}

func applyField(e *Event, f rawXMLField) {
	val := f.scalar()
	switch f.Name {
	case "xml_report":
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
		// Data is applied before actions, so an object event's own
		// database_name field — the object's database — wins over the action,
		// which is the session's current database.
		if e.DatabaseName == "" {
			e.DatabaseName = val
		}
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
	case "object_type":
		// A map field: its value is the numeric key, its text the name.
		e.ObjectType = f.Text
	case "attach_activity_id":
		e.ActivityID, e.ActivitySeq = parseActivityID(val)
	case "event_sequence":
		e.Sequence = parseInt64(val)
	default:
		if e.AdditionalFields == nil {
			e.AdditionalFields = map[string]any{}
		}
		e.AdditionalFields[f.Name] = additionalValue(f.Type, val)
	}
}

func additionalValue(fieldType, value string) any {
	switch strings.ToLower(fieldType) {
	case "boolean", "bool":
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	case "float32", "float64", "double":
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return value
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

// Match whole driver statements so a SET prefix cannot hide later batch SQL.
var noiseStatementRe = regexp.MustCompile(`^(?:` +
	`EXEC SP_UNPREPARE [0-9]+|` +
	`SET (?:QUOTED_IDENTIFIER|ARITHABORT|NUMERIC_ROUNDABORT|ANSI_NULLS|ANSI_NULL_DFLT_ON|ANSI_PADDING|ANSI_WARNINGS|CONCAT_NULL_YIELDS_NULL|CURSOR_CLOSE_ON_COMMIT|IMPLICIT_TRANSACTIONS|NOCOUNT|XACT_ABORT|FMTONLY|NO_BROWSETABLE) (?:ON|OFF)|` +
	`SET (?:TEXTSIZE|LOCK_TIMEOUT|ROWCOUNT) -?[0-9]+|` +
	`SET DATEFIRST [1-7]|SET DATEFORMAT (?:MDY|DMY|YMD|YDM|MYD|DYM)|` +
	`SET LANGUAGE (?:[A-Z_]+|N?'(?:[^']|'')*')|` +
	`SET TRANSACTION ISOLATION LEVEL (?:READ UNCOMMITTED|READ COMMITTED|REPEATABLE READ|SNAPSHOT|SERIALIZABLE)|` +
	`SET DEADLOCK_PRIORITY (?:LOW|NORMAL|HIGH|-?[0-9]+)` +
	`)$`)

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
	return noiseStatementRe.MatchString(upper)
}
