package xetrace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/commons/properties"
	mssql "github.com/microsoft/go-mssqldb"
)

// transientPollErr is the exact failure the retry budget exists for: the poll
// deadline fired mid-read, the driver sent a TDS attention packet, and the
// server never confirmed the cancellation.
func transientPollErr() error {
	return fmt.Errorf("read ring_buffer target: %w", mssql.StreamError{
		InnerError: fmt.Errorf("did not get cancellation confirmation from the server (current response: %w)", context.DeadlineExceeded),
	})
}

// setPollProps shortens the retry pause so a test does not sit out the 2s
// production default, and pins the retry budget the case is exercising.
func setPollProps(t *testing.T, retries string) {
	t.Helper()
	properties.Set("sqltrace.poll.retryDelay", "1ms")
	properties.Set("sqltrace.poll.retries", retries)
	t.Cleanup(func() {
		properties.Set("sqltrace.poll.retryDelay", "")
		properties.Set("sqltrace.poll.retries", "")
	})
}

// scriptedPoller returns each scripted result in turn, then keeps returning the
// last one. A nil error entry yields that step's events.
type scriptedPoller struct {
	mu    sync.Mutex
	steps []scriptedStep
	calls int
}

type scriptedStep struct {
	events []Event
	stats  RingBufferStats
	err    error
}

func (p *scriptedPoller) Poll(context.Context) (RingBufferSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	step := p.steps[len(p.steps)-1]
	if p.calls < len(p.steps) {
		step = p.steps[p.calls]
	}
	p.calls++
	if step.err != nil {
		return RingBufferSnapshot{}, step.err
	}
	return RingBufferSnapshot{Events: step.events, Stats: step.stats}, nil
}

func (p *scriptedPoller) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestDrain_RetriesTransientPollFailure is the regression guard for the
// reported symptom: a single slow ring-buffer read used to abort the whole
// capture. It must now be tolerated, and the events must still arrive.
func TestDrain_RetriesTransientPollFailure(t *testing.T) {
	setPollProps(t, "2")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := mkEvent(1, time.Millisecond, "SELECT survived", t0)

	p := &scriptedPoller{steps: []scriptedStep{
		{err: transientPollErr()},
		{err: transientPollErr()},
		{events: []Event{ev}},
	}}

	var got []Event
	var failures []int
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Drain(ctx, p, DrainOptions{
		Interval:      5 * time.Millisecond,
		OnEvent:       func(e Event) { got = append(got, e) },
		OnPollFailure: func(n int, _ error) { failures = append(failures, n) },
	})
	if err != nil {
		t.Fatalf("two transient failures must be tolerated, got %v", err)
	}
	if len(got) != 1 || got[0].Statement != "SELECT survived" {
		t.Fatalf("expected the event captured after the retries, got %+v", sqls(got))
	}
	if len(failures) < 2 || failures[0] != 1 || failures[1] != 2 {
		t.Errorf("OnPollFailure must report a rising consecutive count, got %v", failures)
	}
}

// TestDrain_GivesUpAfterConsecutiveTransientFailures pins the loud half of the
// contract: tolerating failures forever would be a silent fallback.
func TestDrain_GivesUpAfterConsecutiveTransientFailures(t *testing.T) {
	setPollProps(t, "2")
	p := &scriptedPoller{steps: []scriptedStep{{err: transientPollErr()}}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := Drain(ctx, p, DrainOptions{Interval: 5 * time.Millisecond})
	if err == nil {
		t.Fatal("a persistently failing poll must surface an error")
	}
	if !strings.Contains(err.Error(), "failed 3 times consecutively") {
		t.Errorf("error must name the attempt count, got %q", err)
	}
	// The cause stays reachable by type. Note it is NOT reachable via
	// errors.Is(err, context.DeadlineExceeded): mssql.StreamError has no
	// Unwrap method, so the chain stops there — which is exactly why
	// IsTransientPollError matches on the StreamError type rather than on the
	// deadline hiding inside its message.
	var stream mssql.StreamError
	if !errors.As(err, &stream) {
		t.Errorf("error must keep the underlying cause wrapped, got %q", err)
	}
}

// TestDrain_SuccessResetsFailureCounter guards the difference between "flaky"
// and "broken": intermittent failures either side of a good poll must not
// accumulate into a terminal error.
func TestDrain_SuccessResetsFailureCounter(t *testing.T) {
	setPollProps(t, "1")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	p := &scriptedPoller{steps: []scriptedStep{
		{err: transientPollErr()},
		{events: []Event{mkEvent(1, time.Millisecond, "SELECT one", t0)}},
		{err: transientPollErr()},
		{events: []Event{mkEvent(2, time.Millisecond, "SELECT two", t0.Add(time.Second))}},
	}}

	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: 5 * time.Millisecond,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("alternating failures must not accumulate, got %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both events across the failures, got %v", sqls(got))
	}
}

// TestDrain_TerminalErrorAbortsImmediately pins the allowlist polarity: an
// error that retrying cannot fix must not be retried at all.
func TestDrain_TerminalErrorAbortsImmediately(t *testing.T) {
	setPollProps(t, "5")
	sessionGone := fmt.Errorf("%w: %q", ErrSessionGone, "oipa_cli_trace_1_2")
	p := &scriptedPoller{steps: []scriptedStep{{err: sessionGone}}}

	var retried bool
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := Drain(ctx, p, DrainOptions{
		Interval:      5 * time.Millisecond,
		OnPollFailure: func(int, error) { retried = true },
	})
	if !errors.Is(err, ErrSessionGone) {
		t.Fatalf("expected ErrSessionGone unwrapped, got %v", err)
	}
	if retried {
		t.Error("a terminal error must not be reported as a retry")
	}
	if n := p.callCount(); n != 1 {
		t.Errorf("expected exactly one poll, got %d", n)
	}
}

// TestDrain_RetryPreservesCrossPollState is why the retry lives inside Drain.
// Restarting the loop instead would discard the handle cache and the nester,
// so a prepared handle established before the failure would no longer resolve.
func TestDrain_RetryPreservesCrossPollState(t *testing.T) {
	setPollProps(t, "2")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	prepare := mkEvent(1, time.Millisecond, prepexecOf(
		5089, "@P0 int", "EXEC ASC_GETINTAKERECORDITEMS @P0 ", "1000"), t0)
	prepare.Name = EventRPCCompleted
	deriveFromStatement(&prepare)

	reuse := mkEvent(1, 2*time.Millisecond, "exec sp_execute 5089,2000", t0.Add(time.Second))
	reuse.Name = EventRPCCompleted
	deriveFromStatement(&reuse)

	p := &scriptedPoller{steps: []scriptedStep{
		{events: []Event{prepare}},
		{err: transientPollErr()},
		{events: []Event{reuse}},
	}}

	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: 5 * time.Millisecond,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %v", sqls(got))
	}
	const want = "EXEC ASC_GETINTAKERECORDITEMS 2000"
	if got[1].SQL != want {
		t.Errorf("handle cache did not survive the retry: got %q, want %q", got[1].SQL, want)
	}
}

// TestDrain_ReportsEventsDroppedWhileNotReading is what keeps a tolerated
// failure honest. The ring buffer keeps evicting while we are not reading it,
// and Event.Key dedup cannot tell the difference between "no new events" and
// "the events we missed are gone" — only the server's own running total can.
func TestDrain_ReportsEventsDroppedWhileNotReading(t *testing.T) {
	setPollProps(t, "2")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	p := &scriptedPoller{steps: []scriptedStep{
		{
			events: []Event{mkEvent(1, time.Millisecond, "SELECT first", t0)},
			stats:  RingBufferStats{TotalEventsProcessed: 1, EventCount: 1},
		},
		{err: transientPollErr()},
		{
			// The server dispatched 40 more events; we only see one of them.
			events: []Event{mkEvent(2, time.Millisecond, "SELECT last", t0.Add(time.Second))},
			stats:  RingBufferStats{TotalEventsProcessed: 41, EventCount: 1},
		},
	}}

	var drops []int64
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval:  5 * time.Millisecond,
			OnDropped: func(delta int64, _ RingBufferStats) { drops = append(drops, delta) },
		})
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned %v", err)
	}
	if len(drops) == 0 {
		t.Fatal("a gap between the server's processed count and delivered events must be reported")
	}
	if drops[0] != 39 {
		t.Errorf("expected 39 unseen events (40 dispatched, 1 delivered), got %d", drops[0])
	}
}

// TestDrain_ReportsTruncatedTarget covers the other silent loss: the DMV cut
// target_data short, so the document we parsed is not the whole buffer.
func TestDrain_ReportsTruncatedTarget(t *testing.T) {
	setPollProps(t, "2")
	p := &scriptedPoller{steps: []scriptedStep{{
		stats: RingBufferStats{Truncated: true, DroppedCount: 12, EventCount: 1000},
	}}}

	var seen []RingBufferStats
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval:  5 * time.Millisecond,
			OnDropped: func(_ int64, s RingBufferStats) { seen = append(seen, s) },
		})
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned %v", err)
	}
	if len(seen) == 0 || !seen[0].Truncated {
		t.Fatalf("a truncated ring buffer must be reported, got %+v", seen)
	}
}

// TestDrain_FinalDrainRetriesTransientFailure covers the poll whose failure
// costs the most — everything captured since the last success.
func TestDrain_FinalDrainRetriesTransientFailure(t *testing.T) {
	setPollProps(t, "2")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := mkEvent(9, time.Millisecond, "LATE", t0)

	// Interval of an hour means the ticker never fires: the only polls are the
	// final drain's, so the retry under test is unambiguously that one.
	p := &scriptedPoller{steps: []scriptedStep{
		{err: transientPollErr()},
		{events: []Event{late}},
	}}

	var got []Event
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: time.Hour,
			OnEvent:  func(e Event) { got = append(got, e) },
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("the final drain must retry a transient failure, got %v", err)
	}
	if len(got) != 1 || got[0].Statement != "LATE" {
		t.Fatalf("expected LATE via the retried final drain, got %+v", sqls(got))
	}
}

// TestDrain_OnPollBatchMarksChunkBoundaries pins the hook the Redis event store
// chunks on: one call per successful poll, and none for a failed one.
func TestDrain_OnPollBatchMarksChunkBoundaries(t *testing.T) {
	setPollProps(t, "2")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	p := &scriptedPoller{steps: []scriptedStep{
		{events: []Event{mkEvent(1, time.Millisecond, "SELECT one", t0)}},
		{err: transientPollErr()},
		{events: []Event{mkEvent(2, time.Millisecond, "SELECT two", t0.Add(time.Second))}},
	}}

	var batches [][]string
	var pending []string
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- Drain(ctx, p, DrainOptions{
			Interval: 5 * time.Millisecond,
			OnEvent:  func(e Event) { pending = append(pending, e.Statement) },
			OnPollBatch: func() {
				batches = append(batches, pending)
				pending = nil
			},
		})
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()

	if err := <-doneCh; err != nil {
		t.Fatalf("Drain returned %v", err)
	}
	var nonEmpty [][]string
	for _, b := range batches {
		if len(b) > 0 {
			nonEmpty = append(nonEmpty, b)
		}
	}
	if len(nonEmpty) != 2 {
		t.Fatalf("expected one non-empty batch per successful poll with events, got %v", batches)
	}
	if nonEmpty[0][0] != "SELECT one" || nonEmpty[1][0] != "SELECT two" {
		t.Errorf("batches must arrive in poll order, got %v", nonEmpty)
	}
}
