package xetrace

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakePoller struct {
	mu      sync.Mutex
	batches [][]Event
	calls   int
	err     error
}

func (f *fakePoller) Poll(ctx context.Context) (TargetSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return TargetSnapshot{}, f.err
	}
	if f.calls >= len(f.batches) {
		f.calls++
		return TargetSnapshot{}, nil
	}
	batch := f.batches[f.calls]
	f.calls++
	return TargetSnapshot{Events: batch}, nil
}

func mkEvent(sid int, d time.Duration, stmt string, ts time.Time) Event {
	return Event{SessionID: sid, Duration: d, Statement: stmt, Timestamp: ts}
}

func TestDrain_DeliversDedupedEventsAcrossPolls(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := mkEvent(1, time.Millisecond, "SELECT 1", t0)
	b := mkEvent(2, 2*time.Millisecond, "SELECT 2", t0.Add(time.Second))
	c := mkEvent(3, 3*time.Millisecond, "SELECT 3", t0.Add(2*time.Second))

	p := &fakePoller{batches: [][]Event{
		{a, b},    // first poll
		{a, b, c}, // second poll — a, b are dupes
		{c},       // third poll — c is dupe
	}}

	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: 10 * time.Millisecond,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()

	// Give ticker time for ~3 polls then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 deduped events, got %d: %+v", len(got), got)
	}
	if got[0].Statement != "SELECT 1" || got[1].Statement != "SELECT 2" || got[2].Statement != "SELECT 3" {
		t.Fatalf("wrong order / contents: %+v", got)
	}
}

// runDrainOver feeds the poller's batches through Drain and returns everything
// delivered, in delivery order.
func runDrainOver(t *testing.T, batches [][]Event) []Event {
	t.Helper()
	return runDrainWith(t, batches, DrainOptions{})
}

// runDrainWith is runDrainOver with the caller's options; OnEvent and Interval
// are always set here.
func runDrainWith(t *testing.T, batches [][]Event, opts DrainOptions) []Event {
	t.Helper()
	p := &fakePoller{batches: batches}
	var got []Event
	opts.Interval = 10 * time.Millisecond
	opts.OnEvent = func(e Event) { got = append(got, e) }
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, opts)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	return got
}

// rpcEvent is an rpc_completed carrying stmt, derived the way the parser does.
func rpcEvent(stmt string, at time.Time) Event {
	e := mkEvent(1, time.Millisecond, stmt, at)
	e.Name = EventRPCCompleted
	deriveFromStatement(&e)
	return e
}

// TestDrain_TableFilterMatchesPreparedReruns pins the undercount a `table:`
// filter used to cause: an sp_execute ships a handle and values, no SQL, so a
// filter applied before the handle resolves sees Tables=[sp_execute] and drops
// every re-run of a prepared statement on the filtered table.
func TestDrain_TableFilterMatchesPreparedReruns(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prepare := rpcEvent(prepexecOf(
		5089, "@P0 int", "SELECT StatusCode FROM AsActivity WHERE ActivityID = @P0", "1000"), t0)
	rerun := rpcEvent("exec sp_execute 5089,2000", t0.Add(time.Second))
	other := rpcEvent("SELECT 1 FROM AsPolicy", t0.Add(2*time.Second))

	got := runDrainWith(t, [][]Event{{prepare}, {rerun, other}}, DrainOptions{
		Filter: EventFilter{Tables: []string{"AsActivity"}},
	})

	want := []string{
		"SELECT StatusCode FROM AsActivity WHERE ActivityID = 1000",
		"SELECT StatusCode FROM AsActivity WHERE ActivityID = 2000",
	}
	if diff := sqls(got); !reflect.DeepEqual(diff, want) {
		t.Fatalf("delivered %#v, want %#v", diff, want)
	}
}

// TestDrain_ResolvesHandlesPreparedOutsideTheFilter: the handle cache must see
// every prepare, not only the ones the filter keeps, or a re-run of an
// excluded statement reads as "prepared before the capture" and is miscounted.
func TestDrain_ResolvesHandlesPreparedOutsideTheFilter(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prepare := rpcEvent(prepexecOf(7, "@P0 int", "SELECT 1 FROM AsPolicy WHERE PolicyID = @P0", "1"), t0)
	rerun := rpcEvent("exec sp_execute 7,2", t0.Add(time.Second))

	var unresolved []Event
	got := runDrainWith(t, [][]Event{{prepare}, {rerun}}, DrainOptions{
		Filter:       EventFilter{Tables: []string{"AsActivity"}},
		OnUnresolved: func(e Event) { unresolved = append(unresolved, e) },
	})

	if len(got) != 0 {
		t.Fatalf("delivered %v, want nothing: both statements are on AsPolicy", sqls(got))
	}
	if len(unresolved) != 0 {
		t.Fatalf("reported %d unresolved, want 0: the re-run's handle was prepared inside the capture", len(unresolved))
	}
}

// TestDrain_ReportsRerunsTheFilterCannotPlace: a handle prepared before the
// capture started has no known text, so a table filter cannot say whether it
// belongs. Dropping it silently would undercount; it is reported instead.
func TestDrain_ReportsRerunsTheFilterCannotPlace(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	orphan := rpcEvent("exec sp_execute 91,N'9F1C',100000", t0)

	var unresolved []Event
	got := runDrainWith(t, [][]Event{{orphan}}, DrainOptions{
		Filter:       EventFilter{Tables: []string{"AsActivity"}},
		OnUnresolved: func(e Event) { unresolved = append(unresolved, e) },
	})

	if len(got) != 0 {
		t.Fatalf("delivered %v, want nothing: the filter cannot place the re-run", sqls(got))
	}
	if len(unresolved) != 1 {
		t.Fatalf("reported %d unresolved, want 1", len(unresolved))
	}
}

// TestDrain_DeliversUnresolvedRerunsWithoutAFilter: with nothing to place the
// re-run against, its cost belongs in the capture like any other statement.
func TestDrain_DeliversUnresolvedRerunsWithoutAFilter(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	orphan := rpcEvent("exec sp_execute 91,N'9F1C',100000", t0)

	var unresolved []Event
	got := runDrainWith(t, [][]Event{{orphan}}, DrainOptions{
		OnUnresolved: func(e Event) { unresolved = append(unresolved, e) },
	})

	if len(got) != 1 || len(unresolved) != 0 {
		t.Fatalf("delivered %d / unresolved %d, want 1 / 0", len(got), len(unresolved))
	}
}

// TestDrain_ResolvesPreparedHandleAcrossPolls covers the reason the handle cache
// lives in Drain rather than the parser: the sp_prepexec carrying the SQL text
// and the sp_execute reusing it can land in different ring-buffer reads.
func TestDrain_ResolvesPreparedHandleAcrossPolls(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	prepare := mkEvent(1, time.Millisecond, prepexecOf(
		5089, "@P0 int", "EXEC USP_GETORDERITEMS @P0 ", "1000"), t0)
	prepare.Name = EventRPCCompleted
	deriveFromStatement(&prepare)

	reuse := mkEvent(1, 2*time.Millisecond, "exec sp_execute 5089,2000", t0.Add(time.Second))
	reuse.Name = EventRPCCompleted
	deriveFromStatement(&reuse)

	got := runDrainOver(t, [][]Event{{prepare}, {reuse}})

	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(got), sqls(got))
	}
	const want = "EXEC USP_GETORDERITEMS 2000"
	if got[1].SQL != want {
		t.Errorf("reused handle resolved to %q, want %q", got[1].SQL, want)
	}
	if got[1].ParamsUnavailable {
		t.Error("a resolved handle must not be flagged params-unavailable")
	}
}

// TestDrain_NestsInnerStatementsAfterTheirParent pins the live ordering fix:
// XE completes inner statements before the call containing them, so without the
// nester the stream shows a procedure's body ahead of the procedure.
func TestDrain_NestsInnerStatementsAfterTheirParent(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	child := func(seq int, sql string, at time.Time) Event {
		e := mkEvent(1, time.Millisecond, sql, at)
		e.Name = EventSPStatementCompleted
		e.ActivityID, e.ActivitySeq = "ACT-1", seq
		deriveFromStatement(&e)
		return e
	}
	parent := mkEvent(1, 5*time.Millisecond, "EXEC usp_GetOrderTotals 1", t0.Add(3*time.Second))
	parent.Name = EventRPCCompleted
	parent.ActivityID, parent.ActivitySeq = "ACT-1", 3
	deriveFromStatement(&parent)

	// Children arrive in an earlier poll than the parent, as they do live.
	got := runDrainOver(t, [][]Event{
		{child(1, "SELECT inner one", t0), child(2, "SELECT inner two", t0.Add(time.Second))},
		{parent},
	})

	want := []string{"EXEC usp_GetOrderTotals 1", "SELECT inner one", "SELECT inner two"}
	if diff := sqls(got); !reflect.DeepEqual(diff, want) {
		t.Fatalf("delivery order = %#v, want %#v", diff, want)
	}

	// Nest turns that ordering into containment.
	nested := Nest(got)
	if len(nested) != 1 || len(nested[0].Children) != 2 {
		t.Fatalf("expected one parent owning two statements, got %d top-level", len(nested))
	}
}

// TestDrain_FlushesOrphanedInnerStatements guards against the nester swallowing
// work: if the parent call never arrives — filtered out by --min-duration, or
// completing after the window closed — its statements must still be reported.
func TestDrain_FlushesOrphanedInnerStatements(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	orphan := mkEvent(1, time.Millisecond, "SELECT orphaned", t0)
	orphan.Name = EventSPStatementCompleted
	orphan.ActivityID, orphan.ActivitySeq = "ACT-9", 1
	deriveFromStatement(&orphan)

	got := runDrainOver(t, [][]Event{{orphan}})
	if len(got) != 1 || got[0].SQL != "SELECT orphaned" {
		t.Fatalf("orphaned statement was not flushed, got %v", sqls(got))
	}
}

func TestDrain_ReturnsPollError(t *testing.T) {
	p := &fakePoller{err: errors.New("boom")}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// "boom" is not in the transient allowlist, so it must abort on the first
	// tick and surface UNWRAPPED — callers (registry.runDrain, sql_trace) add
	// their own context, and burying the first message behind a retry count
	// would make a genuine failure harder to read.
	err := Drain(ctx, p, DrainOptions{Interval: 10 * time.Millisecond})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
}

// straddlePoller models the fixed-duration window failure: the first (ticker)
// poll is a slow ring-buffer read that blocks until its context is cancelled by
// the window deadline and then returns a context error; the final drain (run on
// a fresh background context) succeeds and delivers the captured event.
type straddlePoller struct {
	mu    sync.Mutex
	calls int
	final []Event
}

func (p *straddlePoller) Poll(ctx context.Context) (TargetSnapshot, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()
	if n == 0 {
		<-ctx.Done()
		return TargetSnapshot{}, fmt.Errorf("read ring_buffer target: %w", ctx.Err())
	}
	return TargetSnapshot{Events: p.final}, nil
}

// TestDrain_TickerPollDeadlineFallsThroughToFinalDrain reproduces the
// fixed-duration `sql trace` symptom: a ticker poll cancelled by the window
// deadline must not abort Drain with an error — the final drain still runs and
// returns the events captured just before the window closed.
func TestDrain_TickerPollDeadlineFallsThroughToFinalDrain(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := mkEvent(7, time.Millisecond, "LATE", t0)
	p := &straddlePoller{final: []Event{late}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	var got []Event
	err := Drain(ctx, p, DrainOptions{
		Interval: 10 * time.Millisecond,
		OnEvent:  func(e Event) { got = append(got, e) },
	})
	if err != nil {
		t.Fatalf("Drain must not surface the window-deadline poll error, got %v", err)
	}
	if len(got) != 1 || got[0].Statement != "LATE" {
		t.Fatalf("expected LATE delivered via final drain, got %+v", got)
	}
}

func TestDrain_FinalDrainRunsAfterCancel(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := mkEvent(9, time.Millisecond, "LATE", t0)
	// First batch empty, second batch has the late event — but we cancel
	// before the ticker fires, so the late event must come through the
	// final-drain path.
	p := &fakePoller{batches: [][]Event{nil, {late}}}

	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: time.Hour,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()
	// Let one regular poll happen... actually with interval=1h the ticker
	// won't fire. So the first poll never runs, and the final drain (index
	// 0 batch = nil) also delivers nothing from batch[0]. Adjust test: we
	// want the final drain to deliver the first batch on cancel.
	p.batches = [][]Event{{late}}
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if len(got) != 1 || got[0].Statement != "LATE" {
		t.Fatalf("expected LATE delivered via final drain, got %+v", got)
	}
}

// runDrainToFinal cancels before the ticker fires, so batch reaches Drain only
// through the final drain that follows the stop.
func runDrainToFinal(t *testing.T, batch []Event, opts DrainOptions) []Event {
	t.Helper()
	p := &fakePoller{batches: [][]Event{batch}}
	var got []Event
	opts.Interval = time.Hour
	opts.OnEvent = func(e Event) { got = append(got, e) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Drain(ctx, p, opts); err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	return got
}

// A session this process created only holds what happened while it ran, so the
// server's timestamps are not compared with this host's clock: a server clock
// ahead of ours would otherwise drop the last events before the stop.
func TestDrain_KeepsAnOwnedSessionsEventsStampedAfterOurStop(t *testing.T) {
	serverAhead := mkEvent(9, time.Millisecond, "LAST", time.Now().UTC().Add(3*time.Second))

	got := runDrainToFinal(t, []Event{serverAhead}, DrainOptions{})

	if len(got) != 1 || got[0].Statement != "LAST" {
		t.Fatalf("expected LAST delivered, got %+v", got)
	}
}

// An attached session keeps running after the capture, so its stop bound still
// cuts off what happened after the stop.
func TestDrain_BoundsAnAttachedSessionAtTheStop(t *testing.T) {
	now := time.Now().UTC()
	after := mkEvent(9, time.Millisecond, "AFTER", now.Add(time.Minute))

	got := runDrainToFinal(t, []Event{after}, DrainOptions{StartedAt: now.Add(-time.Minute)})

	if len(got) != 0 {
		t.Fatalf("expected nothing after the stop, got %+v", got)
	}
}
