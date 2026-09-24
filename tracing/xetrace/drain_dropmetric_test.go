package xetrace

import (
	"context"
	"testing"
	"time"
)

// snapshotPoller replays a fixed sequence of snapshots, one per Poll, then
// blocks on ctx so Drain's final drain sees the last one again.
type snapshotPoller struct {
	snapshots []RingBufferSnapshot
	at        int
}

func (p *snapshotPoller) Poll(context.Context) (RingBufferSnapshot, error) {
	if p.at < len(p.snapshots) {
		s := p.snapshots[p.at]
		p.at++
		return s, nil
	}
	return p.snapshots[len(p.snapshots)-1], nil
}

func drainOnce(t *testing.T, p poller) (dropped []int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	opts := DrainOptions{
		Interval: time.Millisecond,
		OnEvent:  func(Event) {},
		OnDropped: func(delta int64, _ RingBufferStats) {
			dropped = append(dropped, delta)
		},
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := Drain(ctx, p, opts); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	return dropped
}

// The bug this pins: events we deliberately skip are still events the ring
// buffer handed us. Subtracting only the DELIVERED count from the server's
// totalEventsProcessed reported every filtered event as data loss — on a real
// intake run, ~560 phantom "lost" events per run, none of them real. An
// exclusion must never be reported as an eviction.
func TestDrainDoesNotReportExcludedEventsAsLost(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	// The server dispatched 3 events; we read all 3 but deliver only 1,
	// because 2 were driver chatter / filtered out.
	p := &snapshotPoller{snapshots: []RingBufferSnapshot{
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base, Statement: "SELECT 1 FROM AsPolicy"}},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
				Event{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SET FMTONLY OFF"}.Key(),
			},
			Stats: RingBufferStats{TotalEventsProcessed: 0, EventCount: 3},
		},
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base.Add(3 * time.Millisecond), Statement: "SELECT 2 FROM AsClient"}},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(4 * time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
				Event{Name: "sql_statement_completed", Timestamp: base.Add(5 * time.Millisecond), Statement: "SET FMTONLY OFF"}.Key(),
			},
			Stats: RingBufferStats{TotalEventsProcessed: 3, EventCount: 3},
		},
	}}

	if got := drainOnce(t, p); len(got) != 0 {
		t.Fatalf("reported %v events lost, want none — all 3 were observed, 2 merely excluded", got)
	}
}

// The counterpart: a genuine eviction must still be reported, or fixing the
// false alarm would have made the metric useless. Here the server dispatched
// 10 events between polls but the buffer only ever handed us 3.
func TestDrainStillReportsGenuineEviction(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	p := &snapshotPoller{snapshots: []RingBufferSnapshot{
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base, Statement: "SELECT 1 FROM AsPolicy"}},
			Stats:  RingBufferStats{TotalEventsProcessed: 0, EventCount: 1},
		},
		{
			Events: []Event{
				{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SELECT 2 FROM AsClient"},
				{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SELECT 3 FROM AsClient"},
			},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(3 * time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
			},
			Stats: RingBufferStats{TotalEventsProcessed: 10, EventCount: 3},
		},
	}}

	got := drainOnce(t, p)
	if len(got) == 0 {
		t.Fatal("reported no loss, want the 7 events the buffer evicted unseen")
	}
	// 10 dispatched, 3 observed (2 delivered + 1 excluded) => 7 genuinely lost.
	if got[0] != 7 {
		t.Fatalf("reported %d events lost, want 7", got[0])
	}
}

// Excluded keys are deduplicated the same way delivered events are: the ring
// buffer is cumulative, so the same chatter statement reappears in every poll
// and must be counted as observed exactly once. Counting it per-poll would
// over-credit "observed" and mask real eviction.
func TestDrainDeduplicatesExcludedKeysAcrossPolls(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	chatter := Event{Name: "sql_statement_completed", Timestamp: base, Statement: "SET NOCOUNT ON"}.Key()

	p := &snapshotPoller{snapshots: []RingBufferSnapshot{
		{
			Events:       []Event{{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SELECT 1 FROM AsPolicy"}},
			ExcludedKeys: []string{chatter},
			Stats:        RingBufferStats{TotalEventsProcessed: 0, EventCount: 2},
		},
		{
			// Same chatter event still in the buffer; the server dispatched 3
			// more, of which we see only one new real statement.
			Events:       []Event{{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SELECT 2 FROM AsClient"}},
			ExcludedKeys: []string{chatter},
			Stats:        RingBufferStats{TotalEventsProcessed: 5, EventCount: 2},
		},
	}}

	got := drainOnce(t, p)
	if len(got) == 0 {
		t.Fatal("reported no loss, want the events evicted between polls")
	}
	// 5 dispatched since the first poll; 1 new event observed (the chatter key
	// was already seen) => 4 lost. Re-counting the chatter would report 3.
	if got[0] != 4 {
		t.Fatalf("reported %d events lost, want 4 (excluded keys must dedup)", got[0])
	}
}

// ParseRingBuffer must surface the chatter it drops rather than swallowing it,
// since that silent drop is what made the delta lie in the first place.
func TestParseRingBufferReportsDroppedNoiseAsExcluded(t *testing.T) {
	payload := `<RingBufferTarget truncated="0" eventCount="2" totalEventsProcessed="2">
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:00.000Z">
    <data name="statement"><value>SET NOCOUNT ON</value></data>
  </event>
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:01.000Z">
    <data name="statement"><value>SELECT 1 FROM AsPolicy</value></data>
  </event>
</RingBufferTarget>`

	snapshot, err := ParseRingBuffer(payload)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	if len(snapshot.Events) != 1 {
		t.Fatalf("delivered %d events, want 1", len(snapshot.Events))
	}
	if len(snapshot.ExcludedKeys) != 1 {
		t.Fatalf("reported %d excluded keys, want the 1 dropped chatter statement", len(snapshot.ExcludedKeys))
	}
}
