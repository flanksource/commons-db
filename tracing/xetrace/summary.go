package xetrace

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
)

// pageBytes is SQL Server's fixed 8 KB data page. Every read counter XE
// reports is a page count, so a byte figure only exists as a derivation —
// computed here so the CLI, the API and the web UI all quote the same number.
const pageBytes = 8192

// Summary is the aggregate over one capture: what it cost in time, CPU and IO.
//
// The sums cover TOP-LEVEL events only. When sp_statement_completed is traced,
// causality is on and an rpc_completed's duration/CPU/reads already include
// every inner statement it ran, so adding both would count the same work twice.
// Inner statements are reported separately as Nested. See Accumulator.isInner.
type Summary struct {
	Events int `json:"events"`
	Errors int `json:"errors,omitempty"`
	// Nested is the count of inner statements whose metrics a parent event
	// already accounts for. Non-zero only when sp_statement_completed is traced.
	Nested int `json:"nested,omitempty"`
	// Window is wall-clock first event → last event, which is what the capture
	// actually spans — not the sum of the durations, which overlaps across
	// concurrent sessions.
	Window        time.Duration `json:"window"`
	Duration      time.Duration `json:"duration"`
	AvgDuration   time.Duration `json:"avg_duration"`
	P95Duration   time.Duration `json:"p95_duration"`
	MaxDuration   time.Duration `json:"max_duration"`
	CPUTime       time.Duration `json:"cpu_time"`
	LogicalReads  int64         `json:"logical_reads"`
	PhysicalReads int64         `json:"physical_reads"`
	// ReadBytes is LogicalReads × 8 KB — every page the capture read. A
	// physical read is a page SQL Server first fetched from disk, and that page
	// is then counted as a logical read too, so adding the two would count
	// every disk read twice.
	ReadBytes int64 `json:"read_bytes"`
	Writes    int64 `json:"writes"`
	RowCount  int64 `json:"row_count"`
	// Lost counts events SQL Server raised that the capture never read: evicted
	// from the ring buffer before a poll reached them, or dropped by the server.
	// Every figure above undercounts while it is non-zero.
	Lost int64 `json:"lost,omitempty"`
	// Unresolved counts re-runs of a statement prepared before the capture
	// started, which a type/table filter could not place: their text is
	// unknowable, so their cost is in no figure above either.
	Unresolved int64 `json:"unresolved,omitempty"`
}

// CPURatio is CPU time as a fraction of elapsed time, or 0 when nothing ran.
// Above 1 means the capture ran work in parallel.
func (s Summary) CPURatio() float64 {
	if s.Duration <= 0 {
		return 0
	}
	return float64(s.CPUTime) / float64(s.Duration)
}

// Accumulator folds events into a Summary. It exists so the live server path
// can maintain a running total per poll instead of re-reading and re-summing
// the whole capture on every request.
//
// Not safe for concurrent use; the caller owns the locking.
type Accumulator struct {
	sum         Summary
	durations   []time.Duration
	seenParents map[string]struct{}
	first, last time.Time
}

func NewAccumulator() *Accumulator {
	return &Accumulator{seenParents: map[string]struct{}{}}
}

// Add folds one event, in emission order. Attribution of inner statements
// relies on streamNester having already emitted a parent ahead of the
// statements it ran, which is exactly what Drain delivers. Use AddAll for a
// slice whose order is not guaranteed.
func (a *Accumulator) Add(e Event) {
	a.observeParent(e)
	a.addOne(e, a.isInner(e))
}

// AddAll folds a whole capture. It pre-registers every parent first, so
// attribution does not depend on the input being in emission order.
func (a *Accumulator) AddAll(events []Event) {
	for _, e := range events {
		a.observeParent(e)
	}
	for _, e := range events {
		a.addOne(e, a.isInner(e))
	}
}

// AddLost records events the capture never read (see Summary.Lost). It takes
// the additive deltas DrainOptions.OnDropped reports.
func (a *Accumulator) AddLost(n int64) {
	if n > 0 {
		a.sum.Lost += n
	}
}

// AddUnresolved records one event DrainOptions.OnUnresolved reported.
func (a *Accumulator) AddUnresolved() {
	a.sum.Unresolved++
}

// Result renders the running totals, deriving the figures that can only be
// computed once every event is in (averages, percentile, window).
func (a *Accumulator) Result() Summary {
	out := a.sum
	out.ReadBytes = out.LogicalReads * pageBytes
	if n := len(a.durations); n > 0 {
		out.AvgDuration = out.Duration / time.Duration(n)
		sorted := append([]time.Duration(nil), a.durations...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		out.P95Duration = sorted[p95Index(n)]
	}
	if !a.first.IsZero() && a.last.After(a.first) {
		out.Window = a.last.Sub(a.first)
	}
	return out
}

// Summarize folds a complete slice of events in one call.
func Summarize(events []Event) Summary {
	a := NewAccumulator()
	a.AddAll(events)
	return a.Result()
}

// SummarizeMatching folds only the top-level events whose statement text
// contains pattern — case-insensitive, with runs of whitespace collapsed on
// both sides so a pattern written on one line matches SQL a builder emitted
// across several. The text is the resolved SQL (what a prepared re-run
// actually ran), falling back to the raw statement when nothing was resolved.
// A matching event's inner statements come along as its own, counted but not
// summed again, exactly as Summarize treats them.
//
// Loss is not attributable to a pattern — an event the capture never read has
// no text to match — so Lost and Unresolved stay zero here; a caller reporting
// a subset must carry the whole capture's.
func SummarizeMatching(events []Event, pattern string) Summary {
	needle := strings.ToLower(collapseWhitespace(pattern))
	var matched []Event
	for _, e := range events {
		text := e.SQL
		if text == "" {
			text = e.Statement
		}
		if strings.Contains(strings.ToLower(collapseWhitespace(text)), needle) {
			matched = append(matched, e)
		}
	}
	return Summarize(matched)
}

// observeParent records that an activity has a call that can own statements.
func (a *Accumulator) observeParent(e Event) {
	if e.ActivityID == "" {
		return
	}
	if _, isParent := parentEvents[e.Name]; isParent {
		a.seenParents[e.ActivityID] = struct{}{}
	}
}

// isInner reports whether e's metrics are already included in a parent's.
//
// This mirrors Nest's attribution rule deliberately: same parent kinds, same
// ActivityID grouping, same conservatism — a statement whose parent never
// arrived (filtered out by --min-duration, or completing after the window
// closed) counts as top-level rather than being dropped from the totals.
func (a *Accumulator) isInner(e Event) bool {
	if e.ActivityID == "" {
		return false
	}
	if _, isParent := parentEvents[e.Name]; isParent {
		return false
	}
	_, hasParent := a.seenParents[e.ActivityID]
	return hasParent
}

// addOne records e, summing its metrics unless a parent already accounts for
// them. Children attached by Nest are recorded the same way — counted, never
// summed — so an already-nested slice yields the same totals as a flat one.
func (a *Accumulator) addOne(e Event, inner bool) {
	a.sum.Events++
	if e.ErrorNumber != 0 {
		a.sum.Errors++
	}
	a.observeWindow(e.Timestamp)

	if inner {
		a.sum.Nested++
	} else {
		a.sum.Duration += e.Duration
		a.sum.CPUTime += e.CPUTime
		a.sum.LogicalReads += e.LogicalReads
		a.sum.PhysicalReads += e.PhysicalReads
		a.sum.Writes += e.Writes
		a.sum.RowCount += e.RowCount
		if e.Duration > a.sum.MaxDuration {
			a.sum.MaxDuration = e.Duration
		}
		// An error or a schema change reports no duration; sampling it as 0
		// would drag the average and p95 down.
		if eventHasDuration(e.Name) {
			a.durations = append(a.durations, e.Duration)
		}
	}

	for _, c := range e.Children {
		a.addOne(c, true)
	}
}

func (a *Accumulator) observeWindow(ts time.Time) {
	if ts.IsZero() {
		return
	}
	if a.first.IsZero() || ts.Before(a.first) {
		a.first = ts
	}
	if ts.After(a.last) {
		a.last = ts
	}
}

// p95Index is the nearest-rank 95th percentile position in a sorted slice of n
// values: ceil(0.95·n) − 1. n=1 → 0, n=20 → 18, n=100 → 94.
func p95Index(n int) int {
	return (n*95+99)/100 - 1
}

// Pretty renders the summary as the cost line that follows the "N events
// captured" header, e.g.
//
//	12.4s elapsed · 8.1s cpu (65%) · 4.2M reads (32 GB) · 12K physical · 1.9K writes · 220 rows · avg 9.7ms p95 84ms max 1.2s
func (s Summary) Pretty() api.Text {
	t := api.Text{}.
		Add(api.Human(s.Duration, durationStyle(s.Duration))).
		AddText(" elapsed", "text-muted")

	if s.CPUTime > 0 {
		t = t.AddText(" · ", "text-muted").
			Add(api.Human(s.CPUTime, "text-muted")).
			AddText(" cpu", "text-muted").
			AddText(fmt.Sprintf(" (%.0f%%)", s.CPURatio()*100), "text-muted")
	}

	if s.LogicalReads > 0 {
		t = t.AddText(" · ", "text-muted").
			Add(api.HumanNumber(s.LogicalReads, metricStyle(s.LogicalReads))).
			AddText(" reads (", "text-muted").
			AddText(api.HumanizeBytes(s.ReadBytes).Content, "text-muted").
			AddText(")", "text-muted")
	}
	if s.PhysicalReads > 0 {
		t = t.AddText(" · ", "text-muted").
			Add(api.HumanNumber(s.PhysicalReads, metricStyle(s.PhysicalReads))).
			AddText(" physical", "text-muted")
	}
	if s.Writes > 0 {
		t = t.AddText(" · ", "text-muted").
			Add(api.HumanNumber(s.Writes, metricStyle(s.Writes))).
			AddText(" writes", "text-muted")
	}
	if s.RowCount > 0 {
		t = t.AddText(" · ", "text-muted").
			Add(api.HumanNumber(s.RowCount, "text-muted")).
			AddText(" rows", "text-muted")
	}

	if s.AvgDuration > 0 {
		t = t.AddText(" · ", "text-muted").
			AddText("avg ", "text-muted").Add(api.Human(s.AvgDuration, "text-muted")).
			AddText(" p95 ", "text-muted").Add(api.Human(s.P95Duration, durationStyle(s.P95Duration))).
			AddText(" max ", "text-muted").Add(api.Human(s.MaxDuration, durationStyle(s.MaxDuration)))
	}

	if s.Nested > 0 {
		t = t.AddText(fmt.Sprintf(" · %d nested", s.Nested), "text-muted")
	}
	if s.Lost > 0 {
		t = t.AddText(" · ", "text-muted").AddText(fmt.Sprintf("%d lost", s.Lost), "text-red-500 font-bold")
	}
	if s.Unresolved > 0 {
		t = t.AddText(" · ", "text-muted").AddText(fmt.Sprintf("%d unresolved", s.Unresolved), "text-red-500 font-bold")
	}
	return t
}
