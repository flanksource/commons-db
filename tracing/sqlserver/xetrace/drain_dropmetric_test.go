package xetrace

import (
	"context"
	"testing"
	"time"
)

// snapshotPoller replays a fixed sequence of snapshots, one per Poll, then
// blocks on ctx so Drain's final drain sees the last one again.
type snapshotPoller struct {
	snapshots []TargetSnapshot
	at        int
}

func (p *snapshotPoller) Poll(context.Context) (TargetSnapshot, error) {
	if p.at < len(p.snapshots) {
		s := p.snapshots[p.at]
		p.at++
		return s, nil
	}
	return p.snapshots[len(p.snapshots)-1], nil
}

func drainOnce(t *testing.T, p poller) (dropped []int64) {
	t.Helper()
	return drainOnceWith(t, p, EventFilter{})
}

func drainOnceWith(t *testing.T, p poller, filter EventFilter) (dropped []int64) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	opts := DrainOptions{
		Interval: time.Millisecond,
		Filter:   filter,
		OnEvent:  func(Event) {},
		OnDropped: func(delta int64, _ TargetStats) {
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
	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base, Statement: "SELECT 1 FROM AsPolicy"}},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
				Event{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SET FMTONLY OFF"}.Key(),
			},
			Stats: TargetStats{TotalEventsProcessed: 0, EventCount: 3},
		},
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base.Add(3 * time.Millisecond), Statement: "SELECT 2 FROM AsClient"}},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(4 * time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
				Event{Name: "sql_statement_completed", Timestamp: base.Add(5 * time.Millisecond), Statement: "SET FMTONLY OFF"}.Key(),
			},
			Stats: TargetStats{TotalEventsProcessed: 3, EventCount: 3},
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
	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{
			Events: []Event{{Name: "sql_statement_completed", Timestamp: base, Statement: "SELECT 1 FROM AsPolicy"}},
			Stats:  TargetStats{TotalEventsProcessed: 0, EventCount: 1},
		},
		{
			Events: []Event{
				{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SELECT 2 FROM AsClient"},
				{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SELECT 3 FROM AsClient"},
			},
			ExcludedKeys: []string{
				Event{Name: "sql_statement_completed", Timestamp: base.Add(3 * time.Millisecond), Statement: "SET NOCOUNT ON"}.Key(),
			},
			Stats: TargetStats{TotalEventsProcessed: 10, EventCount: 3},
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

	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{
			Events:       []Event{{Name: "sql_statement_completed", Timestamp: base.Add(time.Millisecond), Statement: "SELECT 1 FROM AsPolicy"}},
			ExcludedKeys: []string{chatter},
			Stats:        TargetStats{TotalEventsProcessed: 0, EventCount: 2},
		},
		{
			// Same chatter event still in the buffer; the server dispatched 3
			// more, of which we see only one new real statement.
			Events:       []Event{{Name: "sql_statement_completed", Timestamp: base.Add(2 * time.Millisecond), Statement: "SELECT 2 FROM AsClient"}},
			ExcludedKeys: []string{chatter},
			Stats:        TargetStats{TotalEventsProcessed: 5, EventCount: 2},
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

// The caller's filter runs inside Drain now, so what it rejects has to count as
// observed there too — the same false alarm TestDrainDoesNotReportExcludedEventsAsLost
// pins for driver chatter.
func TestDrainDoesNotReportFilteredEventsAsLost(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	stmt := func(at time.Duration, sql string) Event {
		e := Event{Name: EventSQLStatementCompleted, Timestamp: base.Add(at), Statement: sql}
		deriveFromStatement(&e)
		return e
	}
	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{
			Events: []Event{stmt(0, "SELECT 1 FROM AsActivity"), stmt(time.Millisecond, "SELECT 1 FROM AsPolicy")},
			Stats:  TargetStats{TotalEventsProcessed: 0, EventCount: 2},
		},
		{
			Events: []Event{stmt(2*time.Millisecond, "SELECT 2 FROM AsActivity"), stmt(3*time.Millisecond, "SELECT 2 FROM AsPolicy")},
			Stats:  TargetStats{TotalEventsProcessed: 2, EventCount: 2},
		},
	}}

	if got := drainOnceWith(t, p, EventFilter{Tables: []string{"AsActivity"}}); len(got) != 0 {
		t.Fatalf("reported %v events lost, want none — the AsPolicy rows were read, just filtered", got)
	}
}

// droppedCount is the server's running total for the session, so forwarding it
// on every poll re-reports the same loss each second. A caller summing the
// deltas into a "lost" figure needs each drop counted once.
func TestDrainReportsServerDropsOnce(t *testing.T) {
	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{Stats: TargetStats{DroppedCount: 5}},
		{Stats: TargetStats{DroppedCount: 5}},
		{Stats: TargetStats{DroppedCount: 8}},
	}}

	var total int64
	for _, delta := range drainOnce(t, p) {
		total += delta
	}
	if total != 8 {
		t.Fatalf("summed drop deltas = %d, want 8 — the server dropped 8 events in all", total)
	}
}

// A pooled connection reset raises 5701 and 5703 in the same millisecond, on
// one session, with no duration and no statement: every field the old key was
// built from is identical. Measured on the lab, that collision was the whole of
// a 10-member intake's reported loss — so the event's sequence must key it.
func TestParseRingBufferKeysEventsBySequence(t *testing.T) {
	payload := `<RingBufferTarget truncated="0" eventCount="2" totalEventsProcessed="2">
  <event name="error_reported" package="sqlserver" timestamp="2026-09-10T16:09:14.356Z">
    <data name="error_number"><value>5701</value></data>
    <action name="session_id" package="sqlserver"><value>89</value></action>
    <action name="event_sequence" package="package0"><value>41</value></action>
  </event>
  <event name="error_reported" package="sqlserver" timestamp="2026-09-10T16:09:14.356Z">
    <data name="error_number"><value>5703</value></data>
    <action name="session_id" package="sqlserver"><value>89</value></action>
    <action name="event_sequence" package="package0"><value>42</value></action>
  </event>
</RingBufferTarget>`

	snapshot, err := ParseRingBuffer(payload)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("parsed %d events, want 2", len(snapshot.Events))
	}
	first, second := snapshot.Events[0], snapshot.Events[1]
	if first.Sequence != 41 || second.Sequence != 42 {
		t.Fatalf("sequences = %d, %d; want 41, 42", first.Sequence, second.Sequence)
	}
	if first.Key() == second.Key() {
		t.Fatalf("two events share key %q; their sequences must tell them apart", first.Key())
	}
}

// Two events identical in everything but their sequence are two events: both
// are delivered, both count as observed, and nothing is reported lost. The
// excluded pair is the other collision the lab showed — a driver's repeated
// `IF @@TRANCOUNT > 0` on one session in one millisecond.
func TestDrainKeepsEventsThatDifferOnlyBySequence(t *testing.T) {
	at := time.Date(2026, 9, 10, 16, 12, 9, 524000000, time.UTC)
	reset := func(seq int64) Event {
		return Event{Name: EventErrorReported, Timestamp: at, SessionID: 88, ErrorNumber: 5701, Sequence: seq}
	}
	chatter := func(seq int64) string {
		return Event{Name: EventRPCCompleted, Timestamp: at, SessionID: 88, Duration: 2 * time.Microsecond, Statement: "IF @@TRANCOUNT > 0", Sequence: seq}.Key()
	}
	p := &snapshotPoller{snapshots: []TargetSnapshot{
		{Stats: TargetStats{TotalEventsProcessed: 0}},
		{
			Events:       []Event{reset(7), reset(8)},
			ExcludedKeys: []string{chatter(9), chatter(10)},
			Stats:        TargetStats{TotalEventsProcessed: 4, EventCount: 4},
		},
	}}

	var delivered int
	var lost []int64
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := Drain(ctx, p, DrainOptions{
		Interval:  time.Millisecond,
		OnEvent:   func(Event) { delivered++ },
		OnDropped: func(delta int64, _ TargetStats) { lost = append(lost, delta) },
	})

	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if delivered != 2 {
		t.Errorf("delivered %d events, want both resets", delivered)
	}
	if len(lost) != 0 {
		t.Errorf("reported %v lost, want none — all 4 dispatched events were read", lost)
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
