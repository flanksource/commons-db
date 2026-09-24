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

func (f *fakePoller) Poll(ctx context.Context) (RingBufferSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return RingBufferSnapshot{}, f.err
	}
	if f.calls >= len(f.batches) {
		f.calls++
		return RingBufferSnapshot{}, nil
	}
	batch := f.batches[f.calls]
	f.calls++
	return RingBufferSnapshot{Events: batch}, nil
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
	p := &fakePoller{batches: batches}
	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: 10 * time.Millisecond,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	return got
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

func (p *straddlePoller) Poll(ctx context.Context) (RingBufferSnapshot, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()
	if n == 0 {
		<-ctx.Done()
		return RingBufferSnapshot{}, fmt.Errorf("read ring_buffer target: %w", ctx.Err())
	}
	return RingBufferSnapshot{Events: p.final}, nil
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
