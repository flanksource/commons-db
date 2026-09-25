package sqltrace

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// fakeXE is an in-memory xeSession that serves pre-loaded events and
// records drop calls. Batches are consumed in order; once exhausted, Poll
// returns nil so the drain loop idles until ctx is cancelled.
type fakeXE struct {
	mu       sync.Mutex
	batches  [][]xetrace.Event
	calls    int
	dropped  bool
	database string
}

func (f *fakeXE) Database() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.database
}

// scopeXE mimics what xetrace.Create does with the database: it honours a name
// the caller gave, and otherwise scopes to the connection's own — "testdb" for
// every registry built here.
func scopeXE(xe *fakeXE, opts xetrace.CreateOptions) *fakeXE {
	if xe == nil {
		return nil
	}
	xe.mu.Lock()
	defer xe.mu.Unlock()
	xe.database = opts.DatabaseName
	if xe.database == "" {
		xe.database = "testdb"
	}
	return xe
}

func (f *fakeXE) Poll(ctx context.Context) (xetrace.RingBufferSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.batches) {
		f.calls++
		return xetrace.RingBufferSnapshot{}, nil
	}
	out := f.batches[f.calls]
	f.calls++
	return xetrace.RingBufferSnapshot{Events: out}, nil
}

func (f *fakeXE) Drop(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = true
	return nil
}

func (f *fakeXE) wasDropped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

// memoryLog is an EventLog held in this process. The registry takes the log as
// an interface, so its own behaviour — the drain, the cursor, the lifecycle —
// is tested without standing up whatever storage a host happens to use.
type memoryLog struct {
	mu     sync.Mutex
	chunks map[string][][]xetrace.Event
}

func testEventStore(t *testing.T) *memoryLog {
	t.Helper()
	return &memoryLog{chunks: map[string][][]xetrace.Event{}}
}

func (l *memoryLog) Append(traceID string, seq int, events []xetrace.Event) error {
	if len(events) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.chunks[traceID]) <= seq {
		l.chunks[traceID] = append(l.chunks[traceID], nil)
	}
	l.chunks[traceID][seq] = append([]xetrace.Event(nil), events...)
	return nil
}

func (l *memoryLog) All(traceID string) ([]xetrace.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []xetrace.Event
	for _, chunk := range l.chunks[traceID] {
		out = append(out, chunk...)
	}
	return out, nil
}

func (l *memoryLog) Since(traceID, sinceKey string) ([]xetrace.Event, error) {
	all, err := l.All(traceID)
	if err != nil || sinceKey == "" {
		return all, err
	}
	for at, e := range all {
		if e.Key() == sinceKey {
			return append([]xetrace.Event(nil), all[at+1:]...), nil
		}
	}
	// An unknown cursor returns everything, as the interface requires.
	return all, nil
}

func (l *memoryLog) Forget(traceID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.chunks, traceID)
}

func (l *memoryLog) Flush() error { return nil }

func newTestRegistry(t *testing.T, xe *fakeXE, name string) *Registry {
	t.Helper()
	r := &Registry{
		traces:  make(map[string]*ActiveTrace),
		dbFor:   func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
		events:  testEventStore(t),
		nowFunc: func() time.Time { return time.Now().UTC() },
		xeFactory: func(_ context.Context, _ *sql.DB, opts xetrace.CreateOptions) (xeSession, string, error) {
			return scopeXE(xe, opts), name, nil
		},
	}
	return r
}

func TestRegistry_StartHoldsSingleDatabaseLeaseUntilStop(t *testing.T) {
	providerCalls := 0
	releaseCalls := 0
	xe := &fakeXE{database: "active-db"}
	r := &Registry{
		traces: make(map[string]*ActiveTrace),
		dbFor: func(context.Context) (*sql.DB, func(), error) {
			providerCalls++
			return nil, func() { releaseCalls++ }, nil
		},
		events:  testEventStore(t),
		nowFunc: func() time.Time { return time.Now().UTC() },
		xeFactory: func(_ context.Context, _ *sql.DB, _ xetrace.CreateOptions) (xeSession, string, error) {
			return xe, "lease-test", nil
		},
	}

	trace, err := r.Start(context.Background(), StartOptions{Poll: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("database provider called %d times, want 1", providerCalls)
	}
	// A caller that names no database is scoped by the session, not by the
	// registry, so the recorded scope must be the one the session resolved.
	if trace.Database != "active-db" {
		t.Fatalf("trace database = %q, want active-db", trace.Database)
	}
	if releaseCalls != 0 {
		t.Fatalf("database lease released before trace stop")
	}

	if _, err := r.Stop(trace.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if releaseCalls != 1 {
		t.Fatalf("database lease release calls = %d, want 1", releaseCalls)
	}
}

func evt(sid int, stmt string, ts time.Time) xetrace.Event {
	return xetrace.Event{
		SessionID: sid,
		Statement: stmt,
		Timestamp: ts,
		Duration:  time.Millisecond,
	}
}

func waitUntilTraceHasEvents(t *testing.T, trace *ActiveTrace, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got int
	for time.Now().Before(deadline) {
		events, err := trace.EventsSince("")
		if err != nil {
			t.Fatalf("EventsSince: %v", err)
		}
		got = len(events)
		if got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("trace never accumulated %d events (got %d)", want, got)
}

func TestRegistry_StartAccumulatesEventsAndStops(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := evt(10, "SELECT 1", t0)
	b := evt(11, "SELECT 2", t0.Add(time.Second))

	xe := &fakeXE{batches: [][]xetrace.Event{{a, b}}}
	r := newTestRegistry(t, xe, "test-session")

	trace, err := r.Start(context.Background(), StartOptions{
		CreateOptions: xetrace.CreateOptions{DatabaseName: "testdb"},
		Poll:          5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitUntilTraceHasEvents(t, trace, 2, time.Second)

	if trace.SessionName != "test-session" {
		t.Fatalf("SessionName = %q, want test-session", trace.SessionName)
	}
	if !trace.Running() {
		t.Fatalf("trace should be running while drain is active")
	}

	stopped, err := r.Stop(trace.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.Running() {
		t.Fatalf("trace.Running() should be false after Stop")
	}
	if !xe.wasDropped() {
		t.Fatalf("XE session.Drop was not called on Stop")
	}
}

// TestRegistry_ShortSpanCapturesViaFinalDrain is the regression guard for the
// apply sql_xevent span being shorter than the poll interval: no background
// poll fires, so Stop()+Result() only sees events if stop() synchronously waits
// for runDrain's final drain. Models a fast apply step (its query completes in
// well under the 1s poll interval).
func TestRegistry_ShortSpanCapturesViaFinalDrain(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := evt(20, "SELECT short", t0)

	xe := &fakeXE{batches: [][]xetrace.Event{{a}}}
	r := newTestRegistry(t, xe, "short-session")

	trace, err := r.Start(context.Background(), StartOptions{
		CreateOptions: xetrace.CreateOptions{DatabaseName: "testdb"},
		Poll:          time.Hour, // guarantees no background poll fires
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped, err := r.Stop(trace.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	result, err := stopped.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(result.Events) != 1 || result.Events[0].Statement != "SELECT short" {
		t.Fatalf("expected 1 event captured via the final drain, got %+v", result.Events)
	}
	if !xe.wasDropped() {
		t.Fatalf("session must be dropped after stop")
	}
}

func TestRegistry_EventsSinceReturnsOnlyNew(t *testing.T) {
	t0 := time.Now().UTC()
	seeded := []xetrace.Event{
		evt(1, "A", t0),
		evt(2, "B", t0.Add(time.Second)),
		evt(3, "C", t0.Add(2*time.Second)),
	}
	store := testEventStore(t)
	// Two chunks, so the cursor is exercised across a chunk boundary the way a
	// real capture produces it (one chunk per poll).
	if err := store.Append("cursor-test", 0, seeded[:2]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Append("cursor-test", 1, seeded[2:]); err != nil {
		t.Fatalf("Append: %v", err)
	}
	trace := &ActiveTrace{ID: "cursor-test", events: store}

	got, err := trace.EventsSince(seeded[0].Key())
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(got) != 2 || got[0].Statement != "B" || got[1].Statement != "C" {
		t.Fatalf("EventsSince(first) = %+v, want [B, C]", got)
	}

	got, err = trace.EventsSince(seeded[1].Key())
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(got) != 1 || got[0].Statement != "C" {
		t.Fatalf("EventsSince(second) = %+v, want [C]", got)
	}

	// Unknown key → fall back to full buffer so the client re-syncs
	// rather than silently missing events.
	got, err = trace.EventsSince("bogus")
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("EventsSince(bogus) returned %d events, want full buffer of 3", len(got))
	}
}

// TestRegistry_DeleteForgetsStoredEvents pins that removing a trace removes its
// events too, rather than leaving chunks nothing refers to until the family TTL
// reclaims them hours later.
func TestRegistry_DeleteForgetsStoredEvents(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	xe := &fakeXE{batches: [][]xetrace.Event{{evt(30, "SELECT gone", t0)}}}
	r := newTestRegistry(t, xe, "forget-test")

	trace, err := r.Start(context.Background(), StartOptions{Poll: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitUntilTraceHasEvents(t, trace, 1, time.Second)

	if _, err := r.Delete(trace.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	left, err := r.events.All(trace.ID)
	if err != nil {
		t.Fatalf("All after Delete: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("Delete left %d event(s) in the store", len(left))
	}
}

func TestRegistry_DeleteStopsAndForgets(t *testing.T) {
	xe := &fakeXE{}
	r := newTestRegistry(t, xe, "delete-test")
	trace, err := r.Start(context.Background(), StartOptions{Poll: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ok, err := r.Delete(trace.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !ok {
		t.Fatalf("Delete returned false for known trace")
	}
	if _, present := r.Get(trace.ID); present {
		t.Fatalf("trace should be absent after Delete")
	}
	if !xe.wasDropped() {
		t.Fatalf("Delete should drop the XE session")
	}
}

func TestRegistry_StopAllDropsEveryTrace(t *testing.T) {
	xe1 := &fakeXE{}
	xe2 := &fakeXE{}
	r := &Registry{
		traces:  make(map[string]*ActiveTrace),
		dbFor:   func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
		events:  testEventStore(t),
		nowFunc: func() time.Time { return time.Now().UTC() },
	}
	// Inject the two fakes via a closure that returns the next one each
	// call.
	calls := 0
	sessions := []*fakeXE{xe1, xe2}
	r.xeFactory = func(_ context.Context, _ *sql.DB, opts xetrace.CreateOptions) (xeSession, string, error) {
		s := sessions[calls]
		calls++
		return s, "s", nil
	}

	if _, err := r.Start(context.Background(), StartOptions{Poll: 5 * time.Millisecond}); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	if _, err := r.Start(context.Background(), StartOptions{Poll: 5 * time.Millisecond}); err != nil {
		t.Fatalf("Start 2: %v", err)
	}

	r.StopAll()
	if !xe1.wasDropped() || !xe2.wasDropped() {
		t.Fatalf("StopAll did not drop all sessions (xe1=%v xe2=%v)", xe1.wasDropped(), xe2.wasDropped())
	}
}

func TestRegistry_GCEvictsOldStoppedTraces(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	r := &Registry{
		traces:    make(map[string]*ActiveTrace),
		dbFor:     func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
		events:    testEventStore(t),
		nowFunc:   func() time.Time { return now },
		xeFactory: defaultXEFactory,
	}
	old := &ActiveTrace{ID: "old", running: false, StoppedAt: now.Add(-time.Hour)}
	fresh := &ActiveTrace{ID: "fresh", running: false, StoppedAt: now.Add(-time.Minute)}
	live := &ActiveTrace{ID: "live", running: true}
	r.traces[old.ID] = old
	r.traces[fresh.ID] = fresh
	r.traces[live.ID] = live

	r.GC()

	if _, ok := r.traces["old"]; ok {
		t.Fatalf("old trace should have been evicted")
	}
	if _, ok := r.traces["fresh"]; !ok {
		t.Fatalf("fresh trace should still be present (within TTL)")
	}
	if _, ok := r.traces["live"]; !ok {
		t.Fatalf("live trace should never be evicted")
	}
}
