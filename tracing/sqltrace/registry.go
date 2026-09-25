// Package sqltrace owns the server-side state for live SQL Server Extended
// Events traces that the web UI tails via polling. One Registry is created
// per oipa-cli serve process and shared across browser tabs — matching the
// shared-observability model used by internal/arthas.
package sqltrace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// traceTTL is how long a stopped trace stays in the registry before being
// evicted. Ten minutes gives operators time to re-open the browser tab and
// still see the final summary without leaking indefinitely.
const traceTTL = 10 * time.Minute

// defaultPollInterval is used when CreateOptions.Poll is unset.
const defaultPollInterval = time.Second

// ActiveTrace is the server-side record of one live or recently-stopped
// XE session. The mu-guarded fields are read by HTTP handlers polling for
// new events while the drain goroutine writes into them.
type ActiveTrace struct {
	ID          string                `json:"id"`
	SessionName string                `json:"sessionName"`
	Database    string                `json:"database"`
	StartedAt   time.Time             `json:"startedAt"`
	StopAt      time.Time             `json:"stopAt,omitzero"`
	StoppedAt   time.Time             `json:"stoppedAt,omitzero"`
	Options     xetrace.CreateOptions `json:"options"`
	Error       string                `json:"error,omitempty"`

	mu sync.Mutex
	xe xeSession
	// events is the Redis-backed log this trace's captured events live in.
	// Nothing is buffered in the process: the drain writes one chunk per poll
	// and every read goes back to the store, so a long capture costs bounded
	// memory and survives the goroutine that produced it.
	events   EventLog
	chunkSeq int
	running  bool
	stopOnce sync.Once
	cancel   context.CancelFunc
	// done is closed by runDrain once it has performed its final drain and
	// dropped the XE session. stop() waits on it so a synchronous
	// Stop()+Result() observes the events captured right before cancellation —
	// critical for spans shorter than the poll interval (e.g. a fast apply
	// step), where no background poll ever fired.
	done chan struct{}
}

// stopDrainSlack is the headroom stopDrainTimeout adds on top of the work it
// actually waits for. Ten seconds covers the two 5s windows go-mssqldb spends
// waiting for the server to confirm a cancelled query before it gives up.
const stopDrainSlack = 10 * time.Second

// stopDrainTimeout bounds how long stop() waits for runDrain to finish. It must
// cover BOTH pieces of work runDrain does before closing trace.done — the final
// drain and then the session Drop, which run back to back — or stop() reports a
// spurious timeout while the XE session is still on the server and the DB lease
// is still held.
func stopDrainTimeout() time.Duration {
	return xetrace.PollTimeout() + xetrace.DropTimeout() + stopDrainSlack
}

// xeSession is the subset of *xetrace.Session that the registry depends
// on. Narrowing the surface lets tests substitute an in-memory fake.
type xeSession interface {
	Poll(ctx context.Context) (xetrace.RingBufferSnapshot, error)
	Drop(ctx context.Context) error
	// Database is the scope the session actually resolved to, which is the
	// caller's when they named one and the connection's own when they did not.
	Database() string
}

// Running reports whether the drain goroutine is still polling.
func (t *ActiveTrace) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// EventsSince returns every event whose Key is newer than sinceKey (i.e.
// everything after the first match). An empty sinceKey returns the full
// buffer. A sinceKey that is not found returns the full buffer too, so the
// caller re-syncs rather than silently missing events.
//
// A store failure is returned, never swallowed: an empty slice would render as
// "this trace captured no events", which is indistinguishable from a real
// empty capture.
func (t *ActiveTrace) EventsSince(sinceKey string) ([]xetrace.Event, error) {
	if t.events == nil {
		return nil, fmt.Errorf("trace %q has no event store", t.ID)
	}
	return t.events.Since(t.ID, sinceKey)
}

// Result renders the final TraceResult for post-run display in the UI
// (via CommandOutput + application/clicky+json). Safe to call while running.
func (t *ActiveTrace) Result() (xetrace.TraceResult, error) {
	if t.events == nil {
		return xetrace.TraceResult{}, fmt.Errorf("trace %q has no event store", t.ID)
	}
	events, err := t.events.All(t.ID)
	if err != nil {
		return xetrace.TraceResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	stopped := t.StoppedAt
	if stopped.IsZero() {
		stopped = time.Now().UTC()
	}
	return xetrace.TraceResult{
		SessionName: t.SessionName,
		Database:    t.Database,
		StartedAt:   t.StartedAt,
		StoppedAt:   stopped,
		Duration:    stopped.Sub(t.StartedAt),
		Events:      events,
		Error:       t.Error,
	}, nil
}

// Err returns the terminal capture or cleanup failure, if one occurred.
func (t *ActiveTrace) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Error == "" {
		return nil
	}
	return errors.New(t.Error)
}

func (t *ActiveTrace) finish(stoppedAt time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	if t.StoppedAt.IsZero() {
		t.StoppedAt = stoppedAt
	}
	if err != nil {
		t.Error = err.Error()
	}
}

// Status is the trace's outcome: when it stopped, and why it failed if it did.
// Both are read under the lock the drain writes them with, so a caller cannot
// see a half-written stop.
func (t *ActiveTrace) Status() (time.Time, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.StoppedAt, t.Error
}

// stop signals the drain loop to cancel and waits (bounded) for runDrain to
// finish its final drain and drop the session, then records StoppedAt. Waiting
// is what makes a synchronous Stop()+Result() see late events from a span
// shorter than the poll interval. Safe to call multiple times; runDrain owns
// the actual session Drop.
func (t *ActiveTrace) stop() error {
	t.stopOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
	})
	if t.done != nil {
		select {
		case <-t.done:
		case <-time.After(stopDrainTimeout()):
			return fmt.Errorf("timed out waiting for drain of session %q", t.SessionName)
		}
	}
	return t.Err()
}

// xeFactory builds the XE session + reports the caller's session_id.
// Pulled out of Start so tests can inject a fake without spinning up a
// real SQL Server connection.
type xeFactory func(ctx context.Context, db *sql.DB, opts xetrace.CreateOptions) (xeSession, string, error)

// defaultXEFactory creates the session on its own dedicated connection, which is
// also where it resolves the database to scope to and the session id to exclude.
func defaultXEFactory(ctx context.Context, db *sql.DB, opts xetrace.CreateOptions) (xeSession, string, error) {
	s, err := xetrace.Create(ctx, db, opts)
	if err != nil {
		return nil, "", err
	}
	return s, s.Name, nil
}

// Registry holds every active and recently-stopped trace for one CLI
// process. Safe for concurrent use by HTTP handlers.
type Registry struct {
	mu        sync.Mutex
	traces    map[string]*ActiveTrace
	dbFor     func(context.Context) (*sql.DB, func(), error) // injected so tests can stub it
	events    EventLog                                       // where captured events are recorded
	nowFunc   func() time.Time                               // overridable for tests
	xeFactory xeFactory                                      // overridable for tests
}

// EventLog is where a Registry records what it captures, and where it reads
// those events back. It is an interface because a capture outlives neither the
// process nor the request that started it: the events have to go somewhere the
// host chooses, and this package should not choose for it.
type EventLog interface {
	// Append stores one poll's events under an increasing sequence number.
	Append(traceID string, seq int, events []xetrace.Event) error

	// All returns every event captured for a trace, in delivery order.
	All(traceID string) ([]xetrace.Event, error)

	// Since returns the events after the one whose Key is sinceKey. An empty
	// cursor returns everything, and a cursor the log no longer holds returns
	// everything rather than nothing — a re-send is visible to a caller, a
	// silent hole is not.
	Since(traceID, sinceKey string) ([]xetrace.Event, error)

	// Forget removes a trace's events.
	Forget(traceID string)

	// Flush settles buffered writes, so a reader sees everything recorded.
	Flush() error
}

// NewRegistry builds a Registry wired to the supplied DB provider and event
// log. dbFor is called once per Start, and its release function runs after the
// trace's final drain and session drop.
//
// Captured events live in the log rather than in this process, so a Registry
// built without one can only fail: Start rejects it rather than pretending to
// capture. Constructing without one is still allowed, so a caller can surface
// the registry's capabilities and fail at Start rather than at construction.
func NewRegistry(dbFor func(context.Context) (*sql.DB, func(), error), events EventLog) *Registry {
	return &Registry{
		traces:    make(map[string]*ActiveTrace),
		dbFor:     dbFor,
		events:    events,
		nowFunc:   func() time.Time { return time.Now().UTC() },
		xeFactory: defaultXEFactory,
	}
}

// StartOptions collapses everything a client can pass to Start.
type StartOptions struct {
	xetrace.CreateOptions
	// Duration bounds the trace. Zero means run until Stop.
	Duration time.Duration
	// Poll is the ring-buffer poll interval. Zero defaults to 1s.
	Poll time.Duration
}

// Start creates an XE session and kicks off the drain goroutine. The
// returned *ActiveTrace is registered before this function returns so
// subsequent List/Get calls are consistent.
func (r *Registry) Start(ctx context.Context, opts StartOptions) (*ActiveTrace, error) {
	if r.dbFor == nil {
		return nil, fmt.Errorf("resolve db: database provider is not configured")
	}
	if r.events == nil {
		return nil, fmt.Errorf("resolve event store: sql trace requires a cache store; configure redis.url or pass --redis-url")
	}
	db, release, err := r.dbFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve db: %w", err)
	}
	if release == nil {
		release = func() {}
	}
	// The id is minted before the session so the Extended Events session on the
	// server carries it: the capture library requires a name, and one naming the
	// trace it belongs to is what lets a DBA reading sys.dm_xe_sessions tie a
	// session back to the trace that created it.
	id := newID()
	if opts.Name == "" {
		opts.Name = "oipa_cli_trace_" + id
	}

	xe, sessionName, err := r.xeFactory(ctx, db, opts.CreateOptions)
	if err != nil {
		release()
		return nil, err
	}
	// Create resolves an unnamed database against its own connection, so the
	// scope is read back from the session rather than asked for a second time.
	opts.DatabaseName = xe.Database()

	now := r.nowFunc()
	trace := &ActiveTrace{
		ID:          id,
		SessionName: sessionName,
		Database:    opts.DatabaseName,
		StartedAt:   now,
		Options:     opts.CreateOptions,
		xe:          xe,
		events:      r.events,
		running:     true,
	}
	if opts.Duration > 0 {
		trace.StopAt = now.Add(opts.Duration)
	}

	drainCtx, cancel := r.runContext(opts.Duration)
	trace.cancel = cancel
	trace.done = make(chan struct{})

	r.mu.Lock()
	r.traces[trace.ID] = trace
	r.mu.Unlock()

	pollInterval := opts.Poll
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	go r.runDrain(drainCtx, trace, pollInterval, release)
	return trace, nil
}

// runContext wires a drain context bounded by duration (when non-zero).
// Extracted so tests can inject deterministic cancellation.
func (r *Registry) runContext(duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), duration)
}

// runDrain owns a trace's poll loop until ctx is done, then performs the final
// drain (inside xetrace.Drain) and drops the XE session, leaving the buffer in
// place for GET /sessions/:id to read. It is the SOLE owner of the session Drop
// and of closing trace.done; stop() only signals cancellation and waits.
//
// Defer order (LIFO): the session is dropped first, then the database lease is
// released, and done is closed LAST — so a caller unblocked by done is
// guaranteed the buffer is final, the session is gone, AND the lease is free.
// Closing done before releasing would let Stop return while release() was still
// running, which is a data race on anything the release closure touches.
func (r *Registry) runDrain(ctx context.Context, trace *ActiveTrace, interval time.Duration, release func()) {
	defer close(trace.done)
	defer release()

	// pending collects one poll's events. OnEvent runs inside Drain's dedup
	// loop with its cross-poll state held, so it must stay a plain append —
	// the store write happens at the batch boundary instead.
	var pending []xetrace.Event
	var writeErr error

	// The batch is handed to the writer rather than stored inline: EventStore
	// .Append blocks once cachestore's bounded async channel backs up, and any
	// stall here lets SQL Server's fixed-size ring buffer overwrite events we
	// have not read yet. See eventWriter.
	writer := newEventWriter(trace.events, trace.ID)

	drainErr := xetrace.Drain(ctx, trace.xe, xetrace.DrainOptions{
		Interval: interval,
		OnEvent:  func(e xetrace.Event) { pending = append(pending, e) },
		OnPollBatch: func() {
			if len(pending) == 0 {
				return
			}
			trace.mu.Lock()
			seq := trace.chunkSeq
			trace.chunkSeq++
			trace.mu.Unlock()
			// Ownership of pending passes to the writer; the next poll builds a
			// fresh slice rather than reusing this backing array.
			writer.Enqueue(seq, pending)
			pending = nil
		},
		OnPollFailure: func(consecutive int, err error) {
			logger.Warnf("sqltrace: poll %d of session %q failed, retrying: %v", consecutive, trace.SessionName, err)
		},
		OnDropped: func(delta int64, stats xetrace.RingBufferStats) {
			logger.Warnf(
				"sqltrace: session %q lost %d event(s) before they could be read (truncated=%v droppedCount=%d); "+
					"raise --min-duration, shorten --poll, or raise sqltrace.ringBuffer.maxEvents",
				trace.SessionName, delta, stats.Truncated, stats.DroppedCount,
			)
		},
	})
	if drainErr != nil {
		drainErr = fmt.Errorf("drain session %q: %w", trace.SessionName, drainErr)
		logger.Warnf("sqltrace: %v", drainErr)
	}
	// Close before Flush, and both before anyone observes the trace as
	// finished: Close drains the queue into the store, Flush pushes the store's
	// write-behind buffer to Redis. stop() unblocks on trace.done and its caller
	// reads the result immediately, so a chunk still queued here would read back
	// as a missing chunk.
	if err := writer.Close(); err != nil {
		writeErr = errors.Join(writeErr, fmt.Errorf("write session %q events: %w", trace.SessionName, err))
		logger.Warnf("sqltrace: %v", writeErr)
	}
	// Drain the write-behind buffer before anyone observes the trace as
	// finished: stop() unblocks on trace.done, and its caller reads the
	// result immediately.
	if err := trace.events.Flush(); err != nil {
		writeErr = errors.Join(writeErr, fmt.Errorf("flush session %q events: %w", trace.SessionName, err))
		logger.Warnf("sqltrace: %v", writeErr)
	}
	var dropErr error
	if trace.xe != nil {
		if err := trace.xe.Drop(context.Background()); err != nil {
			dropErr = fmt.Errorf("drop session %q: %w", trace.SessionName, err)
			logger.Warnf("sqltrace: %v", dropErr)
		}
	}
	trace.finish(r.nowFunc(), errors.Join(drainErr, writeErr, dropErr))
}

// Stop cancels a running trace. Idempotent: stopping an already-stopped
// trace is a no-op.
func (r *Registry) Stop(id string) (*ActiveTrace, error) {
	r.mu.Lock()
	trace, ok := r.traces[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("trace %q not found", id)
	}
	return trace, trace.stop()
}

// Get returns a trace by ID.
func (r *Registry) Get(id string) (*ActiveTrace, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.traces[id]
	return t, ok
}

// List returns a snapshot sorted newest-first.
func (r *Registry) List() []*ActiveTrace {
	r.mu.Lock()
	out := make([]*ActiveTrace, 0, len(r.traces))
	for _, t := range r.traces {
		out = append(out, t)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Delete removes a trace after stopping it if still running. Returns
// false if unknown.
func (r *Registry) Delete(id string) (bool, error) {
	r.mu.Lock()
	trace, ok := r.traces[id]
	if ok {
		delete(r.traces, id)
	}
	r.mu.Unlock()
	if !ok {
		return false, nil
	}
	err := trace.stop()
	// Stop first, then forget: the drain's final flush must land before the
	// chunks are removed, or it would resurrect keys the index no longer lists.
	r.events.Forget(trace.ID)
	return true, err
}

// StopAll tears down every active trace. Intended for server shutdown.
func (r *Registry) StopAll() {
	r.mu.Lock()
	snapshot := make([]*ActiveTrace, 0, len(r.traces))
	for _, t := range r.traces {
		snapshot = append(snapshot, t)
	}
	r.mu.Unlock()
	for _, t := range snapshot {
		if err := t.stop(); err != nil {
			logger.Warnf("sqltrace: stop session %q during shutdown: %v", t.SessionName, err)
		}
	}
}

// GC evicts traces whose StoppedAt is older than traceTTL. Call
// periodically; a sweep on every List call is cheap enough for
// single-process use.
func (r *Registry) GC() {
	cutoff := r.nowFunc().Add(-traceTTL)
	var evicted []string
	r.mu.Lock()
	for id, t := range r.traces {
		t.mu.Lock()
		running := t.running
		stopped := t.StoppedAt
		t.mu.Unlock()
		if !running && !stopped.IsZero() && stopped.Before(cutoff) {
			delete(r.traces, id)
			evicted = append(evicted, id)
		}
	}
	r.mu.Unlock()
	// Drop the events too. The family TTL would eventually reclaim them, but it
	// is measured in hours: evicting here keeps the store in step with the
	// registry instead of leaving chunks no trace refers to.
	for _, id := range evicted {
		r.events.Forget(id)
	}
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
