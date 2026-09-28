package xetrace

import (
	"context"
	"fmt"
	"time"
)

// poller is the subset of *Session that Drain needs. Keeping the surface
// small lets tests substitute an in-memory fake without spinning up a DB.
type poller interface {
	Poll(ctx context.Context) (TargetSnapshot, error)
}

// DrainOptions configures the poll loop. Every field is optional; zero values
// fall back to the package defaults in properties.go.
type DrainOptions struct {
	// Interval is the ring-buffer poll cadence. Defaults to one second.
	Interval time.Duration
	// StartedAt bounds passive sessions whose target can contain older events.
	// The exclusive stop bound is captured when ctx ends.
	StartedAt  time.Time
	FinalDelay time.Duration
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
	//
	// Every delta is additive — the events lost since the previous report — so
	// a caller can sum them into a capture's total loss. A truncated target is
	// reported even when it carries no count of its own.
	OnDropped func(delta int64, stats TargetStats)
	// Filter narrows delivery by every supported event dimension. It is
	// applied here rather than as each ring-buffer read is parsed because only
	// here has an sp_execute been resolved to the statement it re-ran: before
	// that, every re-run of a prepared statement reads as Tables=[sp_execute]
	// and a table pattern would drop it. The zero value delivers everything.
	Filter EventFilter
	// OnUnresolved receives each event Filter excluded only because its text is
	// unknowable — an sp_execute of a handle prepared before the capture
	// started. Its cost is real but cannot be placed, so a caller that sums the
	// delivered events must count these rather than let them vanish.
	OnUnresolved func(Event)
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
	// lastDropped does the same for droppedCount, which is also a running total.
	var lastDropped int64
	var stoppedAt time.Time

	emit := func(e Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}

	// deliver returns how many new events it observed — delivered or filtered
	// out alike, since the buffer handed over both and only the eviction delta
	// should count as lost.
	deliver := func(events []Event) int64 {
		var observed int64
		for _, e := range events {
			key := e.Key()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			observed++
			if !opts.StartedAt.IsZero() && e.Timestamp.Before(opts.StartedAt) {
				continue
			}
			if !stoppedAt.IsZero() && !e.Timestamp.Before(stoppedAt) {
				continue
			}
			// Every prepare is cached, including ones the filter rejects: a later
			// re-run of it must resolve to its text, not read as prepared before
			// the capture.
			handles.Observe(e)
			unresolved := handles.Resolve(&e)
			if !opts.Filter.match(e) {
				if unresolved && opts.OnUnresolved != nil {
					opts.OnUnresolved(e)
				}
				continue
			}
			nester.Add(e, emit)
		}
		return observed
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

	reportDrops := func(observed int64, stats TargetStats) {
		dropped := max(stats.DroppedCount-lastDropped, 0)
		lastDropped = stats.DroppedCount
		if opts.OnDropped == nil {
			return
		}
		if stats.Truncated || dropped > 0 {
			opts.OnDropped(dropped, stats)
		}
		if haveProcessed && stats.TotalEventsProcessed > lastProcessed {
			if delta := stats.TotalEventsProcessed - lastProcessed - observed - dropped; delta > 0 {
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
		visible := make(map[string]struct{}, len(snapshot.Events)+len(snapshot.ExcludedKeys))
		for _, e := range snapshot.Events {
			visible[e.Key()] = struct{}{}
		}
		for _, key := range snapshot.ExcludedKeys {
			visible[key] = struct{}{}
		}
		seen = visible
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
			stoppedAt = time.Now().UTC()
			// Events completing just before Stop may still be in SQL Server's
			// dispatch buffer rather than the target we are about to read.
			if opts.FinalDelay > 0 {
				time.Sleep(opts.FinalDelay)
			}
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
