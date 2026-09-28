// Package sqltrace runs live SQL Server Extended Events captures: it creates the
// XE session, drains its target, and commits every captured event as a row of
// a sql_xevent record stream, which is where every reader — a result profile,
// a follower, a step's checkpoint window — reads them back from.
package sqltrace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// defaultPollInterval is used when StartOptions.Poll is unset.
const defaultPollInterval = time.Second

// stopDrainSlack is the headroom stopDrainTimeout adds on top of the work it
// actually waits for. Ten seconds covers the two 5s windows go-mssqldb spends
// waiting for the server to confirm a cancelled query before it gives up.
const stopDrainSlack = 10 * time.Second

// stopDrainTimeout bounds how long stop() waits for runDrain to finish. It must
// cover BOTH pieces of work runDrain does before closing trace.done — the final
// drain and then the session Drop — or stop() reports a spurious timeout while
// the XE session is still on the server and the DB lease is still held.
func stopDrainTimeout(finalDelay time.Duration) time.Duration {
	return finalDelay + xetrace.PollTimeout() + xetrace.DropTimeout() + stopDrainSlack
}

// XESession is the slice of *xetrace.Session a capture drives.
type XESession interface {
	Poll(ctx context.Context) (xetrace.TargetSnapshot, error)
	Drop(ctx context.Context) error
}

// Opened is what an XEFactory reports about the session it created: its name
// and the statements that created and started it.
type Opened struct {
	Name       string
	Statements []string
	FinalDelay time.Duration
}

// XEFactory creates the XE session for a capture.
type XEFactory func(ctx context.Context, db *sql.DB, opts xetrace.CreateOptions) (XESession, Opened, error)

// CreateXESession is the production XEFactory: a session on db, over the ring
// buffer or — when opts.File is set — a .xel event_file target on the server,
// or the built-in system_health session when opts.Session names it.
func CreateXESession(ctx context.Context, db *sql.DB, opts xetrace.CreateOptions) (XESession, Opened, error) {
	var session *xetrace.Session
	var err error
	if opts.Session == xetrace.SystemHealthSession {
		session, err = xetrace.AttachSystemHealth(ctx, db, opts)
	} else {
		session, err = xetrace.Create(ctx, db, opts)
	}
	if err != nil {
		return nil, Opened{}, err
	}
	return session, Opened{Name: session.Name, Statements: session.Statements, FinalDelay: session.FinalDelay()}, nil
}

// CurrentDatabase is the production database resolver: DB_NAME() of one of
// db's connections.
func CurrentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	return xetrace.CurrentDatabase(ctx, conn)
}

// RegistryOptions are the seams a Registry needs. Every one is required.
type RegistryOptions struct {
	// DB leases the context's write pool for one capture; the release runs
	// after the capture's final drain and session drop.
	DB func(context.Context) (*sql.DB, func(), error)
	// Store is where captured rows are committed.
	Store RecordStore
	// NewSession creates the XE session (CreateXESession in production).
	NewSession XEFactory
	// CurrentDatabase names the context's database, which a capture scopes to
	// when it names none (CurrentDatabase in production).
	CurrentDatabase func(context.Context, *sql.DB) (string, error)
}

// Registry starts and stops XE captures. One is built per capture start, over
// the record store the starting request's environment routes to.
type Registry struct {
	opts    RegistryOptions
	nowFunc func() time.Time

	mu     sync.Mutex
	traces map[string]*ActiveTrace
}

// NewRegistry builds a Registry over opts, refusing a missing seam.
func NewRegistry(opts RegistryOptions) (*Registry, error) {
	for _, seam := range []struct {
		missing bool
		name    string
	}{
		{opts.DB == nil, "database provider"},
		{opts.Store == nil, "record store"},
		{opts.NewSession == nil, "session factory"},
		{opts.CurrentDatabase == nil, "database resolver"},
	} {
		if seam.missing {
			return nil, fmt.Errorf("sql trace registry: the %s is not configured", seam.name)
		}
	}
	return &Registry{opts: opts, nowFunc: func() time.Time { return time.Now().UTC() }, traces: map[string]*ActiveTrace{}}, nil
}

// StartOptions collapses everything a caller can pass to Start. An XE session
// is server-wide: CreateOptions.Databases narrows it in SQL Server's own
// predicate, and an empty Databases scopes the capture to the context's
// database.
type StartOptions struct {
	xetrace.CreateOptions
	// Duration bounds the trace. Zero means run until Stop.
	Duration time.Duration
	// Poll is the ring-buffer poll interval. Zero defaults to 1s.
	Poll time.Duration
}

// Start creates the XE session, opens its record stream and starts the drain.
// Every store call the capture makes runs under ctx's values with its
// cancellation detached, so the rows reach the environment ctx names even after
// the request that started the capture has ended.
func (r *Registry) Start(ctx context.Context, opts StartOptions) (*ActiveTrace, error) {
	db, release, err := r.opts.DB(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve db: %w", err)
	}
	if release == nil {
		return nil, errors.New("resolve db: the database provider returned a lease with no release")
	}
	databases, err := r.scope(ctx, db, opts.Databases)
	if err != nil {
		release()
		return nil, err
	}
	opts.Databases = databases
	if len(opts.Filter.Databases) == 0 && (opts.Session == xetrace.SystemHealthSession || slices.Contains(opts.Events, xetrace.EventXMLDeadlockReport)) {
		opts.Filter.Databases = append([]string(nil), databases...)
	}
	xe, opened, err := r.opts.NewSession(ctx, db, opts.CreateOptions)
	if err != nil {
		release()
		return nil, err
	}
	drainCtx, cancel := runContext(opts.Duration)
	trace, err := r.newTrace(ctx, db, opts, xe, opened, cancel)
	if err != nil {
		cancel()
		dropErr := xe.Drop(context.WithoutCancel(ctx))
		release()
		return nil, errors.Join(err, dropErr)
	}
	r.mu.Lock()
	r.traces[trace.ID] = trace
	r.mu.Unlock()

	poll := opts.Poll
	if poll <= 0 {
		poll = defaultPollInterval
	}
	go r.runDrain(drainCtx, trace, poll, release)
	return trace, nil
}

// scope is the database patterns a capture's session is predicated on: the
// caller's own, or, when it names none, the context's database. Every pattern
// set is also pushed to SQL Server for managed sessions, while Go remains the
// authoritative filter seam for managed and attached sessions alike.
func (r *Registry) scope(ctx context.Context, db *sql.DB, databases []string) ([]string, error) {
	if len(databases) > 0 {
		return databases, nil
	}
	name, err := r.opts.CurrentDatabase(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("resolve context database: %w", err)
	}
	if name == "" {
		return nil, errors.New("resolve context database: DB_NAME() named no database")
	}
	return []string{name}, nil
}

func (r *Registry) newTrace(
	ctx context.Context, db *sql.DB, opts StartOptions, xe XESession, opened Opened, cancel context.CancelFunc,
) (*ActiveTrace, error) {
	now := r.nowFunc()
	trace := &ActiveTrace{
		ID: rand.Text(), SessionName: opened.Name, Statements: opened.Statements,
		Database: strings.Join(opts.Databases, ", "), StartedAt: now,
		Options: opts.CreateOptions, xe: xe, running: true, cancel: cancel, done: make(chan struct{}), finalDelay: opened.FinalDelay,
	}
	if opts.Duration > 0 {
		trace.StopAt = now.Add(opts.Duration)
	}
	trace.appender = recordAppender{store: r.opts.Store, ctx: context.WithoutCancel(ctx), stream: trace.ID, db: db}
	writer, err := newEventWriter(trace.appender, trace.failCapture)
	if err != nil {
		return nil, err
	}
	trace.writer = writer
	return trace, nil
}

// runContext bounds the drain by duration, when non-zero.
func runContext(duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), duration)
}

// runDrain owns a trace's poll loop until ctx is done, then performs the final
// drain (inside xetrace.Drain), commits every queued row, seals the stream and
// drops the XE session. It is the SOLE owner of the session Drop and of closing
// trace.done; stop() only signals cancellation and waits.
//
// Defer order (LIFO): the database lease is released before done is closed, so
// a caller unblocked by done is guaranteed the rows are committed, the stream is
// sealed, the session is gone AND the lease is free.
func (r *Registry) runDrain(ctx context.Context, trace *ActiveTrace, interval time.Duration, release func()) {
	defer close(trace.done)
	defer release()

	// pending collects one poll's events. OnEvent runs inside Drain's dedup
	// loop with its cross-poll state held, so it stays a plain append; the
	// batch is handed to the writer at the poll boundary.
	var pending []xetrace.Event
	writer := trace.writer
	startedAt := time.Time{}
	if trace.Options.Session == xetrace.SystemHealthSession {
		startedAt = trace.StartedAt
	}
	drainErr := xetrace.Drain(ctx, trace.xe, xetrace.DrainOptions{
		Interval:     interval,
		StartedAt:    startedAt,
		FinalDelay:   trace.finalDelay,
		Filter:       trace.Options.Filter,
		OnEvent:      func(e xetrace.Event) { pending = append(pending, e) },
		OnUnresolved: func(xetrace.Event) { writer.AddUnresolved() },
		OnPollBatch: func() {
			writer.Enqueue(pending)
			pending = nil
		},
		OnPollFailure: func(consecutive int, err error) {
			logger.Warnf("sqltrace: poll %d of session %q failed, retrying: %v", consecutive, trace.SessionName, err)
		},
		OnDropped: func(delta int64, stats xetrace.TargetStats) {
			// The summary is what a consumer reads, so the loss has to reach it
			// and not only this log line: every figure undercounts by it.
			writer.AddLost(delta)
			logger.Warnf(
				"sqltrace: session %q lost %d event(s) before they could be read (truncated=%v droppedCount=%d); "+
					"raise --min-duration, shorten --poll, or raise sqltrace.ringBuffer.maxEvents",
				trace.SessionName, delta, stats.Truncated, stats.DroppedCount,
			)
		},
	})
	if drainErr != nil {
		drainErr = fmt.Errorf("drain session %q: %w", trace.SessionName, drainErr)
	}
	err := errors.Join(drainErr, closeDrained(trace))
	if err != nil {
		logger.Warnf("sqltrace: %v", err)
	}
	trace.finish(r.nowFunc(), err)
}

// closeDrained ends a drained trace in order: the writer commits every queued
// row, then the stream is sealed, then the XE session is dropped.
func closeDrained(trace *ActiveTrace) error {
	var writeErr error
	if err := trace.writer.Close(); err != nil {
		writeErr = fmt.Errorf("record session %q events: %w", trace.SessionName, err)
	}
	sealErr := trace.appender.Seal()
	var dropErr error
	if err := trace.xe.Drop(context.Background()); err != nil {
		dropErr = fmt.Errorf("drop session %q: %w", trace.SessionName, err)
	}
	return errors.Join(writeErr, sealErr, dropErr)
}

// Stop cancels a trace and waits for its final drain. Idempotent.
func (r *Registry) Stop(id string) (*ActiveTrace, error) {
	r.mu.Lock()
	trace, ok := r.traces[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("trace %q not found", id)
	}
	return trace, trace.stop()
}

// StopAll stops every trace this registry started.
func (r *Registry) StopAll() {
	r.mu.Lock()
	snapshot := make([]*ActiveTrace, 0, len(r.traces))
	for _, trace := range r.traces {
		snapshot = append(snapshot, trace)
	}
	r.mu.Unlock()
	for _, trace := range snapshot {
		if err := trace.stop(); err != nil {
			logger.Warnf("sqltrace: stop session %q: %v", trace.SessionName, err)
		}
	}
}

// Checkpoint blocks until every row captured so far has been committed and
// returns the stream's ref over them. See eventWriter.Checkpoint.
func (t *ActiveTrace) Checkpoint(ctx context.Context) (query.EventsRef, error) {
	return t.writer.Checkpoint(ctx)
}

// EventsRef is the stream's ref as of its last commit, without waiting.
func (t *ActiveTrace) EventsRef() query.EventsRef { return t.writer.EventsRef() }

// Preview returns the latest committed events whose seq is within from..to; a
// bound of 0 is open.
func (t *ActiveTrace) Preview(from, to int64) []xetrace.Event { return t.writer.Preview(from, to) }

// Summary is the IO/CPU/timing aggregate over every committed event.
func (t *ActiveTrace) Summary() xetrace.Summary { return t.writer.Summary() }

// Done is closed once the capture has finished: rows committed, stream sealed,
// session dropped and lease released.
func (t *ActiveTrace) Done() <-chan struct{} { return t.done }
