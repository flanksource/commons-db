// Package xetrace manages short-lived SQL Server Extended Events sessions
// backed by a ring_buffer or event_file target, and attaches to the built-in
// system_health session. Transport and session registries belong to callers.
package xetrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrSessionGone reports that the XE session vanished from
// sys.dm_xe_sessions mid-capture. The DMV row (and its ring_buffer target row,
// with eventCount="0") exists from the moment ALTER … STATE = START returns, so
// its absence never means "not ready yet" — the session was dropped by another
// admin, or the instance restarted or failed over. Retrying can never produce
// data, so this is deliberately NOT a transient poll error.
var ErrSessionGone = errors.New("event session is no longer present in sys.dm_xe_sessions (dropped externally, or the instance restarted)")

// SystemHealthSession is the built-in session CreateOptions.Session can attach
// to instead of creating one.
const SystemHealthSession = "system_health"

// Event names supported by CreateOptions.Events.
const (
	EventSQLStatementCompleted = "sql_statement_completed"
	EventRPCCompleted          = "rpc_completed"
	EventSQLBatchCompleted     = "sql_batch_completed"
	EventErrorReported         = "error_reported"
	// EventSPStatementCompleted fires once per statement executed INSIDE a
	// stored procedure — the only way to see a procedure's internals, which
	// rpc_completed reports as a single aggregate. It is opt-in (absent from
	// DefaultEvents): a busy instance emits it for every statement of every
	// proc, which displaces the calls the user asked for in a ring buffer
	// capped at MaxEvents.
	EventSPStatementCompleted = "sp_statement_completed"
	// The object events report one committed schema change each — the object's
	// name, type and database — whatever statement or batch made it. They are
	// opt-in through Events.
	EventObjectCreated     = "object_created"
	EventObjectAltered     = "object_altered"
	EventObjectDeleted     = "object_deleted"
	EventXMLDeadlockReport = "xml_deadlock_report"
)

// ObjectEvents are the schema-change events callers can select for capture.
var ObjectEvents = []string{EventObjectCreated, EventObjectAltered, EventObjectDeleted}

// AllDatabases is the CreateOptions.Databases value that captures every
// database on the instance.
const AllDatabases = "*"

// DispatchLatency is the MAX_DISPATCH_LATENCY applied to every
// session's target. SQL Server buffers events internally and only
// flushes them to the target after this window elapses (or when a buffer
// fills), so a caller that stops a session MUST wait out this latency to
// capture a span shorter than it — otherwise the final poll reads an empty
// buffer. 1 second is SQL Server's documented minimum for a ring_buffer target.
const DispatchLatency = 1 * time.Second

// SupportedEvents is every XE event name CreateOptions.Events accepts. A name
// outside this set is rejected by NormalizeEvents rather than emitted into DDL
// that SQL Server refuses at CREATE time.
var SupportedEvents = []string{
	EventSQLStatementCompleted,
	EventRPCCompleted,
	EventSQLBatchCompleted,
	EventErrorReported,
	EventSPStatementCompleted,
	EventObjectCreated,
	EventObjectAltered,
	EventObjectDeleted,
	EventXMLDeadlockReport,
}

// DefaultEvents is the set captured when CreateOptions.Events is empty. It is
// deliberately NOT every supported event: EventSPStatementCompleted is opt-in
// and must be named explicitly (`--event`), because capturing it changes both
// the volume and the shape of every trace; the ObjectEvents are opt-in too.
var DefaultEvents = []string{
	EventSQLStatementCompleted,
	EventRPCCompleted,
	EventSQLBatchCompleted,
	EventErrorReported,
}

// NormalizeEvents resolves a caller-supplied event MultiFilter against
// SupportedEvents with collections.MatchItems semantics: a plain name must be
// supported, a `*` / prefix / suffix pattern selects the supported events it
// matches, exclusions win, and an exclusion-only list is DefaultEvents minus the
// exclusions (e.g. `!error_reported`). The result is a de-duplicated, concrete,
// lower-case list; one that selects nothing is an error. An empty input returns
// nil, leaving Create to apply DefaultEvents.
func NormalizeEvents(names []string) ([]string, error) {
	lowered := make([]string, len(names))
	for i, name := range names {
		lowered[i] = strings.ToLower(name)
	}
	patterns, err := parseMatchPatterns(lowered)
	if err != nil {
		return nil, fmt.Errorf("event filter: %w", err)
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	var selected, exclusions []matchPattern
	for _, p := range patterns {
		if !p.wildcard() && !slices.Contains(SupportedEvents, p.value) {
			return nil, fmt.Errorf("unsupported event %q: supported events are %s", p.value, strings.Join(SupportedEvents, ", "))
		}
		if p.negate {
			exclusions = append(exclusions, p)
		} else {
			selected = append(selected, p)
		}
	}
	pool := SupportedEvents
	if len(selected) == 0 {
		selected, pool = []matchPattern{{value: "*"}}, DefaultEvents
	}
	var out []string
	for _, p := range selected {
		names := pool
		if !p.wildcard() {
			names = []string{p.value}
		}
		for _, name := range names {
			excluded := slices.ContainsFunc(exclusions, func(x matchPattern) bool { return x.matches(name) })
			if p.matches(name) && !excluded && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("event filter %q selects no event: supported events are %s", strings.Join(names, ","), strings.Join(SupportedEvents, ", "))
	}
	return out, nil
}

// CreateOptions configures a new Extended Events session.
type CreateOptions struct {
	// Session attaches to a built-in session instead of creating one. The only
	// supported value is system_health.
	Session string
	// Name of the XE session. Required: there is no default.
	//
	// An Extended Events session is a server-scoped object — it shows up in
	// sys.dm_xe_sessions for every DBA on the instance, alongside sessions
	// created by anything else. A name this package invented would tell them
	// which library made it and nothing about which application, so the name
	// belongs to the caller who can answer that.
	Name string
	// Databases scopes the session by database name using clicky MultiFilter
	// values with collections.MatchItems semantics (case-insensitive exact,
	// prefix/suffix `*`, `!` exclusion, comma lists). Translated into the XE
	// WHERE predicate, as are Users, Apps and Hosts, so the filter runs in SQL
	// Server before the target, not post-capture in Go.
	//
	// Empty scopes to the connection's own database, which Create resolves with
	// DB_NAME(); AllDatabases ("*") captures the whole instance. Instance-wide
	// is the wider, more surprising scope, so it is the one that has to be
	// asked for.
	Databases []string
	// Users scopes the session by SQL login with the same patterns as
	// Databases. Empty captures all users.
	Users []string
	// MinDurationMicros filters out events faster than this threshold.
	// Only applied to duration-bearing events (statement/rpc/batch).
	MinDurationMicros int64
	// Events is the list of XE event names to capture. When empty,
	// DefaultEvents is used.
	Events []string
	// ExcludeSessionID is populated by Create from its dedicated connection.
	ExcludeSessionID int
	// Apps scopes the session by client application name with the same
	// patterns as Databases. Empty captures every application.
	Apps []string
	// Hosts scopes the session by client host name with the same patterns as
	// Databases. Empty captures every host.
	Hosts []string
	// MaxMemoryKB is the ring buffer size. Zero uses the
	// sqltrace.ringBuffer.maxMemoryKb property. Meaningless with File set.
	MaxMemoryKB int
	// MaxEvents caps the ring buffer event count. Zero uses the
	// sqltrace.ringBuffer.maxEvents property. Meaningless with File set.
	MaxEvents int
	// File selects a package0.event_file target instead of the ring buffer:
	// events are written to .xel files on the SQL Server host and read back
	// incrementally, so a high-volume capture cannot lose events to the ring
	// buffer's FIFO eviction. Nil keeps the ring buffer.
	File *FileTarget
	// Filter narrows captured events by statement type / referenced table. XE
	// carries no structured type/object info to predicate on at the session
	// level, so it is recorded here with the rest of the capture's configuration
	// and applied by Drain — pass it as DrainOptions.Filter — which is the first
	// point an sp_execute has been resolved to the statement it re-ran. Zero
	// value matches everything. It does not change the XE events captured.
	Filter EventFilter
}

// DrainFilter is Filter as Drain applies it. A deadlock report is added to the
// session without a predicate, and system_health's events never pass one of
// ours, so for a capture that reads either the filter is scoped to Databases,
// unless it names databases of its own.
func (o CreateOptions) DrainFilter() EventFilter {
	filter := o.Filter
	if len(filter.Databases) == 0 && (o.Session == SystemHealthSession || slices.Contains(o.Events, EventXMLDeadlockReport)) {
		filter.Databases = slices.Clone(o.Databases)
	}
	return filter
}

// Session represents a live XE session this process reads: one it created, or
// the built-in system_health session it attached to.
type Session struct {
	Name string
	// Statements are the CREATE EVENT SESSION and START statements Create
	// ran, in order, exactly as it executed them.
	Statements []string
	// FilePath is where an event_file session writes, as resolved on the
	// server. Empty for a ring_buffer session.
	FilePath        string
	db              *sql.Conn
	pool            *sql.DB
	opts            CreateOptions
	fileCursor      fileCursor
	filePattern     string
	owned           bool
	dispatchLatency time.Duration
	droppedBaseline int64
}

// Create builds an XE session with a ring_buffer or event_file target, starts
// it, and returns a Session handle. Callers MUST defer s.Drop to avoid leaking
// the session on the server.
func Create(ctx context.Context, pool *sql.DB, opts CreateOptions) (_ *Session, err error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("create event session: a session name is required")
	}
	db, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	opts.ExcludeSessionID, err = CurrentSessionID(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(opts.Databases) == 0 {
		name, err := CurrentDatabase(ctx, db)
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, errors.New("resolve connection database: DB_NAME() named no database")
		}
		opts.Databases = []string{name}
	}
	permissions, err := CheckPermissions(ctx, db)
	if err != nil {
		return nil, err
	}
	if !permissions.Granted {
		return nil, &PermissionError{Report: permissions}
	}

	if len(opts.Events) == 0 {
		opts.Events = DefaultEvents
	}
	if opts.File == nil {
		if opts.MaxMemoryKB == 0 {
			opts.MaxMemoryKB = defaultRingBufferMemoryKB()
		}
		if opts.MaxEvents == 0 {
			opts.MaxEvents = defaultRingBufferEvents()
		}
	} else {
		file := *opts.File
		if file.MaxFileSizeMB == 0 {
			file.MaxFileSizeMB = defaultEventFileSizeMB()
		}
		if file.MaxRolloverFiles == 0 {
			file.MaxRolloverFiles = defaultEventFileRollovers()
		}
		path, err := ResolveEventFilePath(ctx, db, file, opts.Name)
		if err != nil {
			return nil, err
		}
		file.Path = path
		opts.File = &file
	}

	ddl, err := BuildCreateSQL(opts)
	if err != nil {
		return nil, err
	}

	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return nil, fmt.Errorf("create event session %q: %w", opts.Name, err)
	}

	start := fmt.Sprintf("ALTER EVENT SESSION %s ON SERVER STATE = START", quoteIdent(opts.Name))
	if _, err := db.ExecContext(ctx, start); err != nil {
		// An event_file session fails here, not at CREATE, when the directory is
		// not writable — name it, because the path is on the server and the
		// account that has to write it is the SQL Server service account, not
		// the login this capture authenticated as.
		if opts.File != nil {
			err = fmt.Errorf("%w (the SQL Server service account must be able to write %s)", err, opts.File.Path)
		}
		cleanupErr := (&Session{Name: opts.Name, db: db, pool: pool, owned: true}).Drop(ctx)
		if cleanupErr != nil {
			return nil, fmt.Errorf(
				"start event session %q: %w; cleanup failed: %v",
				opts.Name,
				err,
				cleanupErr,
			)
		}
		return nil, fmt.Errorf("start event session %q: %w", opts.Name, err)
	}

	session := &Session{Name: opts.Name, Statements: []string{ddl, start}, db: db, pool: pool, opts: opts, owned: true, dispatchLatency: DispatchLatency}
	if opts.File != nil {
		session.FilePath = opts.File.Path
	}
	return session, nil
}

// DrainFilter is the filter to pass as DrainOptions.Filter: the session's
// CreateOptions.Filter, scoped to the databases Create resolved for the events
// the session predicate cannot scope.
func (s *Session) DrainFilter() EventFilter { return s.opts.DrainFilter() }

// dropTimeout bounds Session.Drop. Named (and exposed via DropTimeout) because
// a caller waiting for a drain to finish has to budget for the final poll AND
// this drop, which run back to back.
const dropTimeout = 10 * time.Second

// Drop stops and removes the session. Safe to call on a nil receiver. Uses a
// fresh timeout-bounded context so it still runs during shutdown when the
// caller context has already been cancelled. A session this process attached
// to rather than created is only released, never dropped.
func (s *Session) Drop(parent context.Context) error {
	if s == nil {
		return nil
	}
	// A cancelled DMV read can invalidate the pinned connection. Release it
	// before using the pool so teardown can reconnect when necessary.
	if s.db != nil {
		_ = s.db.Close()
	}
	if !s.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
	defer cancel()
	_ = parent // retained for signature symmetry; we intentionally do not use it
	stmt := fmt.Sprintf("DROP EVENT SESSION %s ON SERVER", quoteIdent(s.Name))
	if _, err := s.pool.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop event session %q: %w", s.Name, err)
	}
	return nil
}

// DispatchLatency is the maximum time a final event can remain buffered before
// the session's target receives it.
func (s *Session) DispatchLatency() time.Duration {
	if s == nil {
		return 0
	}
	return s.dispatchLatency
}

// dispatchMargin is the headroom FinalDelay adds over the dispatch latency, for
// the final read's own round trip to catch the last flush.
const dispatchMargin = 250 * time.Millisecond

// FinalDelay is how long a caller waits after stopping before the final read,
// so events that completed just before the stop have left SQL Server's
// dispatch buffer for the target. Pass it as DrainOptions.FinalDelay.
func (s *Session) FinalDelay() time.Duration {
	return s.DispatchLatency() + dispatchMargin
}

// Poll reads the session's target — the whole ring buffer, or the .xel rows an
// event_file target has flushed since the previous poll — and returns the
// parsed events plus the target's own bookkeeping. Callers are responsible for
// deduplication via Event.Key across polls: both targets can hand the same
// event to two consecutive polls.
func (s *Session) Poll(ctx context.Context) (TargetSnapshot, error) {
	if s.db == nil {
		if err := s.reconnect(ctx); err != nil {
			return TargetSnapshot{}, err
		}
	}
	snapshot, err := s.readTarget(ctx)
	if err != nil && (IsTransientPollError(err) || errors.Is(err, context.Canceled)) {
		_ = s.db.Close()
		s.db = nil
	}
	return snapshot, err
}

func (s *Session) readTarget(ctx context.Context) (TargetSnapshot, error) {
	if s.opts.File != nil || s.filePattern != "" {
		return s.pollFile(ctx)
	}
	const q = `SELECT CAST(target_data AS NVARCHAR(MAX)) AS target_data
FROM sys.dm_xe_sessions s
JOIN sys.dm_xe_session_targets t ON t.event_session_address = s.address
WHERE s.name = @p1 AND t.target_name = 'ring_buffer'`

	// NullString, not string: the DMV reports a target that has not produced
	// any data yet as NULL, which a bare string scan rejects outright with
	// "converting NULL to string is unsupported".
	var payload sql.NullString
	if err := s.db.QueryRowContext(ctx, q, s.Name).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TargetSnapshot{}, fmt.Errorf("%w: %q", ErrSessionGone, s.Name)
		}
		return TargetSnapshot{}, fmt.Errorf("read ring_buffer target: %w", err)
	}
	if !payload.Valid || payload.String == "" {
		return TargetSnapshot{}, nil
	}
	return ParseRingBuffer(payload.String)
}

// reconnect pins a fresh reader connection. For a session this process created
// it also updates each event's reader exclusion to the new connection, with a
// brief capture gap per event while its predicate is replaced; an attached
// session is not ours to alter.
func (s *Session) reconnect(ctx context.Context) error {
	db, err := s.pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reconnect %s reader: %w", s.Name, err)
	}
	if !s.owned {
		s.db = db
		return nil
	}
	opts := s.opts
	opts.ExcludeSessionID, err = CurrentSessionID(ctx, db)
	if err != nil {
		_ = db.Close()
		return err
	}
	events, err := NormalizeEvents(opts.Events)
	if err != nil {
		_ = db.Close()
		return err
	}
	filters, err := sessionFilters(opts)
	if err != nil {
		_ = db.Close()
		return err
	}
	for _, event := range events {
		// A deadlock report carries no session-level predicate to update.
		if event == EventXMLDeadlockReport {
			continue
		}
		var ddl strings.Builder
		fmt.Fprintf(&ddl, "IF EXISTS (SELECT 1 FROM sys.server_event_session_events e JOIN sys.server_event_sessions s ON s.event_session_id = e.event_session_id WHERE s.name = N'%s' AND e.name = N'%s')\n", escapeSQLStringLiteral(s.Name), event)
		fmt.Fprintf(&ddl, "ALTER EVENT SESSION %s ON SERVER DROP EVENT sqlserver.%s;\n", quoteIdent(s.Name), event)
		fmt.Fprintf(&ddl, "ALTER EVENT SESSION %s ON SERVER ", quoteIdent(s.Name))
		writeSessionEvent(&ddl, event, opts.MinDurationMicros, filters)
		if _, err := db.ExecContext(ctx, ddl.String()); err != nil {
			_ = db.Close()
			return fmt.Errorf("update reconnected reader exclusion: %w", err)
		}
	}
	s.db, s.opts = db, opts
	return nil
}

// CurrentSessionID returns the sqlserver session_id of the current connection,
// so callers can pass it as CreateOptions.ExcludeSessionID.
func CurrentSessionID(ctx context.Context, db *sql.Conn) (int, error) {
	var sid int
	if err := db.QueryRowContext(ctx, "SELECT @@SPID").Scan(&sid); err != nil {
		return 0, fmt.Errorf("query @@SPID: %w", err)
	}
	return sid, nil
}

// CurrentDatabase returns DB_NAME() for the current connection.
func CurrentDatabase(ctx context.Context, db *sql.Conn) (string, error) {
	var name string
	if err := db.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&name); err != nil {
		return "", fmt.Errorf("query DB_NAME(): %w", err)
	}
	return name, nil
}

// quoteIdent wraps a SQL Server identifier in brackets, escaping any embedded
// closing bracket. Used for session names, which we generate ourselves but
// still want to keep safe.
func quoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}
