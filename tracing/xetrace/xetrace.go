// Package xetrace manages short-lived SQL Server Extended Events sessions
// backed by a ring_buffer target. Migrated from mission-control-oipa's
// internal/database/xetrace; transport and session registries belong to callers.
package xetrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSessionGone reports that the XE session vanished from
// sys.dm_xe_sessions mid-capture. The DMV row (and its ring_buffer target row,
// with eventCount="0") exists from the moment ALTER … STATE = START returns, so
// its absence never means "not ready yet" — the session was dropped by another
// admin, or the instance restarted or failed over. Retrying can never produce
// data, so this is deliberately NOT a transient poll error.
var ErrSessionGone = errors.New("event session is no longer present in sys.dm_xe_sessions (dropped externally, or the instance restarted)")

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
)

// RingBufferDispatchLatency is the MAX_DISPATCH_LATENCY applied to every
// session's ring_buffer target. SQL Server buffers events internally and only
// flushes them to the ring buffer after this window elapses (or when a buffer
// fills), so a caller that stops a session MUST wait out this latency to
// capture a span shorter than it — otherwise the final poll reads an empty
// buffer. 1 second is SQL Server's documented minimum for a ring_buffer target.
const RingBufferDispatchLatency = 1 * time.Second

// SupportedEvents is every XE event name CreateOptions.Events accepts. A name
// outside this set is rejected by NormalizeEvents rather than emitted into DDL
// that SQL Server refuses at CREATE time.
var SupportedEvents = []string{
	EventSQLStatementCompleted,
	EventRPCCompleted,
	EventSQLBatchCompleted,
	EventErrorReported,
	EventSPStatementCompleted,
}

// DefaultEvents is the set captured when CreateOptions.Events is empty. It is
// deliberately NOT every supported event: EventSPStatementCompleted is opt-in
// and must be named explicitly (`--event`), because capturing it changes both
// the volume and the shape of every trace.
var DefaultEvents = []string{
	EventSQLStatementCompleted,
	EventRPCCompleted,
	EventSQLBatchCompleted,
	EventErrorReported,
}

// NormalizeEvents canonicalises a caller-supplied event list: comma-joined
// values are flattened (so a repeated flag and a comma list are equivalent),
// names are trimmed, lower-cased and de-duplicated, and an unknown name is a
// hard error. An empty list returns nil, leaving Create to apply DefaultEvents.
func NormalizeEvents(names []string) ([]string, error) {
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range names {
		for _, part := range strings.Split(raw, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			if !slices.Contains(SupportedEvents, name) {
				return nil, fmt.Errorf("unsupported event %q: supported events are %s", strings.TrimSpace(part), strings.Join(SupportedEvents, ", "))
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out, nil
}

// CreateOptions configures a new Extended Events session.
type CreateOptions struct {
	// Name of the XE session. If empty, a unique name is generated.
	Name string
	// DatabaseName scopes the session to one database; empty uses DB_NAME().
	DatabaseName string
	// Users scopes the session by SQL login using collections.MatchItem
	// patterns (exact, `*` wildcard, `!` exclusion; repeatable). Empty captures
	// all users. Translated into the XE WHERE predicate so the filter runs in
	// SQL Server, not post-capture in Go.
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
	// MatchItem patterns as Users. Empty captures every application.
	Apps []string
	// Hosts scopes the session by client host name with the same MatchItem
	// patterns as Users. Empty captures every host.
	Hosts []string
	// MaxMemoryKB is the ring buffer size. Defaults to 4096 when zero.
	MaxMemoryKB int
	// MaxEvents caps the ring buffer event count. Defaults to 8092 when zero.
	MaxEvents int
	// Filter narrows captured events by statement type / referenced table after
	// parsing. Applied in Poll because XE carries no structured type/object info
	// to predicate on at the session level. Zero value matches everything.
	Filter EventFilter
}

// Session represents a live XE session owned by this process.
type Session struct {
	Name string
	db   *sql.Conn
	pool *sql.DB
	opts CreateOptions
}

// Create builds an XE session with a ring_buffer target, starts it, and
// returns a Session handle. Callers MUST defer s.Drop to avoid leaking the
// session on the server.
func Create(ctx context.Context, pool *sql.DB, opts CreateOptions) (_ *Session, err error) {
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
	if opts.DatabaseName == "" {
		opts.DatabaseName, err = CurrentDatabase(ctx, db)
		if err != nil {
			return nil, err
		}
	}
	permissions, err := CheckPermissions(ctx, db)
	if err != nil {
		return nil, err
	}
	if !permissions.Granted {
		return nil, &PermissionError{Report: permissions}
	}

	if opts.Name == "" {
		opts.Name = "commons_db_trace_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if len(opts.Events) == 0 {
		opts.Events = DefaultEvents
	}
	if opts.MaxMemoryKB == 0 {
		opts.MaxMemoryKB = defaultRingBufferMemoryKB()
	}
	if opts.MaxEvents == 0 {
		opts.MaxEvents = defaultRingBufferEvents()
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
		cleanupErr := (&Session{Name: opts.Name, db: db, pool: pool}).Drop(ctx)
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

	return &Session{Name: opts.Name, db: db, pool: pool, opts: opts}, nil
}

// dropTimeout bounds Session.Drop. Named (and exposed via DropTimeout) because
// a caller waiting for a drain to finish has to budget for the final poll AND
// this drop, which run back to back.
const dropTimeout = 10 * time.Second

// Drop stops and removes the session. Safe to call on a nil receiver. Uses a
// fresh timeout-bounded context so it still runs during shutdown when the
// caller context has already been cancelled.
func (s *Session) Drop(parent context.Context) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
	defer cancel()
	// A cancelled DMV read can invalidate the pinned connection. Release it
	// before using the pool so teardown can reconnect when necessary.
	_ = s.db.Close()
	_ = parent // retained for signature symmetry; we intentionally do not use it
	stmt := fmt.Sprintf("DROP EVENT SESSION %s ON SERVER", quoteIdent(s.Name))
	if _, err := s.pool.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop event session %q: %w", s.Name, err)
	}
	return nil
}

// Poll reads the current ring_buffer contents and returns the parsed events
// plus the target's own bookkeeping. Callers are responsible for deduplication
// via Event.Key across polls.
func (s *Session) Poll(ctx context.Context) (RingBufferSnapshot, error) {
	const q = `SELECT CAST(target_data AS NVARCHAR(MAX)) AS target_data
FROM sys.dm_xe_sessions s
JOIN sys.dm_xe_session_targets t ON t.event_session_address = s.address
WHERE s.name = @p1 AND t.target_name = 'ring_buffer'`

	// NullString, not string: the DMV reports a target that has not produced
	// any data yet as NULL, which a bare string scan rejects outright with
	// "converting NULL to string is unsupported".
	var payload sql.NullString
	row := s.db.QueryRowContext(ctx, q, s.Name)
	if err := row.Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RingBufferSnapshot{}, fmt.Errorf("%w: %q", ErrSessionGone, s.Name)
		}
		return RingBufferSnapshot{}, fmt.Errorf("read ring_buffer target: %w", err)
	}
	if !payload.Valid || payload.String == "" {
		return RingBufferSnapshot{}, nil
	}
	snapshot, err := ParseRingBuffer(payload.String)
	if err != nil {
		return RingBufferSnapshot{}, err
	}
	// Filtering is an exclusion, not a loss: record what it removed so Drain's
	// eviction delta stays a measure of the ring buffer rather than of our own
	// filter. See RingBufferSnapshot.ExcludedKeys.
	kept := s.opts.Filter.Apply(snapshot.Events)
	if len(kept) != len(snapshot.Events) {
		keep := make(map[string]struct{}, len(kept))
		for _, e := range kept {
			keep[e.Key()] = struct{}{}
		}
		for _, e := range snapshot.Events {
			key := e.Key()
			if _, ok := keep[key]; !ok {
				snapshot.ExcludedKeys = append(snapshot.ExcludedKeys, key)
			}
		}
	}
	snapshot.Events = kept
	return snapshot, nil
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
