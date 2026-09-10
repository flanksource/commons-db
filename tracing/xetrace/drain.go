package xetrace

import (
	"context"
	"fmt"
	"time"
)

// poller is the subset of *Session that Drain needs. Keeping the surface
// small lets tests substitute an in-memory fake without spinning up a DB.
type poller interface {
	Poll(ctx context.Context) (RingBufferSnapshot, error)
}

// DrainOptions configures the poll loop. Every field is optional; zero values
// fall back to the package defaults in properties.go.
type DrainOptions struct {
	// Interval is the ring-buffer poll cadence. Defaults to one second.
	Interval time.Duration
	// OnEvent receives each new, deduplicated event in delivery order. It is
	// invoked synchronously while dedup state is held — keep it fast (an append
	// or a channel send, never a network round-trip).
	OnEvent func(Event)
	// OnPollBatch fires once after each poll's events have all been delivered,
	// marking a natural batch boundary for a caller that persists in chunks.
	// It does NOT fire for a poll that failed.
	OnPollBatch func()
	// OnPollFailure reports a transient poll failure that is being retried,
	// with the number of consecutive failures so far. xetrace has no logger of
	// its own (DB chatter flows through gormlog), so callers do the logging.
	OnPollFailure func(consecutive int, err error)
	// OnDropped reports that the server dispatched more events to the ring
	// buffer than we read back, or that it truncated/dropped events itself.
	// This is how a tolerated poll failure stays auditable instead of silently
	// losing the events the buffer evicted while we were not reading it.
	OnDropped func(delta int64, stats RingBufferStats)
}

// Drain polls p at the configured interval, deduplicates events via Event.Key,
// and delivers each new event to opts.OnEvent. It keeps running until ctx is
// cancelled, then performs a final drain so late-arriving events captured
// right before cancellation are not lost.
//
// Two pieces of cross-poll state ride along, because both span a boundary a
// single poll cannot see:
//
//   - a HandleCache, so an sp_execute prepared in one poll still resolves to
//     its statement text when executed in the next;
//   - a streamNester, so a procedure's inner statements are delivered after the
//     call that ran them rather than before it.
//
// Both survive a tolerated poll failure, which is the main reason the retry
// lives here rather than around Session.Poll or around Drain itself: restarting
// the loop would discard them and re-deliver the whole ring buffer as new.
//
// The drain poll uses a bounded background context (not ctx) so shutdown
// still completes when the caller's context is already Done.
func Drain(ctx context.Context, p poller, opts DrainOptions) error {
	interval := opts.Interval
	if interval <= 0 {
		interval = time.Second
	}
	timeout := PollTimeout()
	retries := pollRetries()

	seen := make(map[string]struct{})
	handles := NewHandleCache()
	nester := newStreamNester(0)
	// lastProcessed tracks the server's own running total so an eviction we
	// never read shows up as a delta rather than as nothing at all.
	var lastProcessed int64
	var haveProcessed bool

	emit := func(e Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}

	deliver := func(events []Event) int64 {
		var delivered int64
		for _, e := range events {
			key := e.Key()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			delivered++
			handles.Observe(e)
			handles.Resolve(&e)
			nester.Add(e, emit)
		}
		return delivered
	}

	// observe marks keys we read but deliberately did not deliver — driver
	// chatter and caller-filtered events. They count towards "observed" for the
	// eviction delta, because the buffer did hand them to us; only events that
	// never reached any poll are lost. Skipping this step makes the delta report
	// our own filter's output as data loss.
	observe := func(keys []string) int64 {
		var n int64
		for _, k := range keys {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			n++
		}
		return n
	}

	reportDrops := func(observed int64, stats RingBufferStats) {
		if opts.OnDropped == nil {
			return
		}
		if stats.Truncated || stats.DroppedCount > 0 {
			opts.OnDropped(stats.DroppedCount, stats)
		}
		if haveProcessed && stats.TotalEventsProcessed > lastProcessed {
			if delta := stats.TotalEventsProcessed - lastProcessed - observed; delta > 0 {
				opts.OnDropped(delta, stats)
			}
		}
	}

	drain := func(ctxPoll context.Context) error {
		snapshot, err := p.Poll(ctxPoll)
		if err != nil {
			return err
		}
		observed := deliver(snapshot.Events) + observe(snapshot.ExcludedKeys)
		reportDrops(observed, snapshot.Stats)
		lastProcessed, haveProcessed = snapshot.Stats.TotalEventsProcessed, true
		if opts.OnPollBatch != nil {
			opts.OnPollBatch()
		}
		return nil
	}

	// finalDrain runs on a fresh background context so it still completes when
	// the caller's context is already Done. It gets one extra attempt because
	// its failure costs the most: everything captured since the last success.
	finalDrain := func() error {
		var err error
		for attempt := 0; attempt <= 1; attempt++ {
			pollCtx, cancel := context.WithTimeout(context.Background(), timeout)
			err = drain(pollCtx)
			cancel()
			if err == nil || !IsTransientPollError(err) {
				return err
			}
			if opts.OnPollFailure != nil {
				opts.OnPollFailure(attempt+1, err)
			}
		}
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			// Events completing just before Stop may still be in SQL Server's
			// dispatch buffer rather than the ring target we are about to read.
			time.Sleep(RingBufferDispatchLatency + 250*time.Millisecond)
			err := finalDrain()
			// Statements whose parent never arrived must still be reported.
			nester.Flush(emit)
			return err
		case <-ticker.C:
			pollCtx, cancel := context.WithTimeout(ctx, timeout)
			err := drain(pollCtx)
			cancel()
			if err == nil {
				consecutive = 0
				continue
			}
			// A ticker poll that straddles ctx's deadline/cancellation fails
			// with context.DeadlineExceeded/Canceled. That is the shutdown
			// signal, not a capture failure — fall through to the final
			// drain (which uses a fresh background context) so events
			// captured right before the window closed still land. This check
			// MUST stay ahead of classification: a parent deadline and a poll
			// deadline are the same error value, and only the parent's state
			// tells them apart.
			if ctx.Err() != nil {
				continue
			}
			if !IsTransientPollError(err) {
				nester.Flush(emit)
				return err
			}
			consecutive++
			if opts.OnPollFailure != nil {
				opts.OnPollFailure(consecutive, err)
			}
			if consecutive > retries {
				nester.Flush(emit)
				return fmt.Errorf("ring buffer poll failed %d times consecutively: %w", consecutive, err)
			}
			// Pause before re-attempting: the connection the driver just binned
			// is gone, but whatever made the read slow usually has not passed.
			select {
			case <-ctx.Done():
			case <-time.After(pollRetryDelay()):
			}
		}
	}
}
