// The aggregate over a capture's statements: what they cost, which ran again
// with other values, and the tables and ops they span.

package sqlstatements

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/flanksource/clicky/api"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// maxRepeats caps the repeated statements a Summary lists. Repeated still
// counts every statement of every repeated group.
const maxRepeats = 20

// Summary is the aggregate over a capture's statements, as xetrace.Summary is
// over an XE capture's events. Durations are milliseconds, as a row's are.
type Summary struct {
	Statements int `json:"statements"`
	Errors     int `json:"errors,omitempty"`
	// DurationMs sums every statement's duration; statements on concurrent
	// connections overlap, so it can exceed WindowMs.
	DurationMs    float64 `json:"durationMs"`
	AvgDurationMs float64 `json:"avgDurationMs"`
	P95DurationMs float64 `json:"p95DurationMs"`
	MaxDurationMs float64 `json:"maxDurationMs"`
	// Rows sums the rows the statements returned, leaving out the statements
	// whose driver could not count them.
	Rows int64 `json:"rows"`
	// WindowMs is wall clock from the first statement's start to the last
	// statement's end.
	WindowMs float64 `json:"windowMs"`
	// Repeated counts the statements whose normalized text (RepeatKey) ran
	// more than once: an N+1 shows here. Repeats lists those groups, the most
	// run first, at most maxRepeats of them.
	Repeated int      `json:"repeated,omitempty"`
	Repeats  []Repeat `json:"repeats,omitempty"`
	// Tables are the distinct tables the statements named, sorted.
	Tables []string `json:"tables"`
	// ByOp counts the statements by op.
	ByOp map[string]int `json:"byOp"`
}

// Repeat is one statement text that ran more than once: the first SQL seen
// with its key, how often it ran and what all of its runs took.
type Repeat struct {
	Key        string  `json:"key"`
	SQL        string  `json:"sql"`
	Count      int     `json:"count"`
	DurationMs float64 `json:"durationMs"`
}

// Accumulator folds statements into a Summary, so a capture keeps a running
// total rather than re-reading its stream. Not safe for concurrent use.
type Accumulator struct {
	sum         Summary
	durations   []float64
	tables      map[string]struct{}
	repeats     map[string]*Repeat
	first, last time.Time
}

func NewAccumulator() *Accumulator {
	return &Accumulator{
		sum:    Summary{ByOp: map[string]int{}},
		tables: map[string]struct{}{}, repeats: map[string]*Repeat{},
	}
}

// Add folds one statement.
func (a *Accumulator) Add(row StatementRow) {
	a.sum.Statements++
	if row.Error != "" {
		a.sum.Errors++
	}
	a.sum.DurationMs += row.DurationMs
	a.sum.MaxDurationMs = max(a.sum.MaxDurationMs, row.DurationMs)
	a.durations = append(a.durations, row.DurationMs)
	if row.Rows > 0 {
		a.sum.Rows += row.Rows
	}
	a.sum.ByOp[row.Op]++
	for _, table := range row.Tables {
		a.tables[table] = struct{}{}
	}
	repeat, ok := a.repeats[row.RepeatKey]
	if !ok {
		repeat = &Repeat{Key: row.RepeatKey, SQL: row.SQL}
		a.repeats[row.RepeatKey] = repeat
	}
	repeat.Count++
	repeat.DurationMs += row.DurationMs
	a.observeWindow(row)
}

func (a *Accumulator) observeWindow(row StatementRow) {
	if row.StartedAt.IsZero() {
		return
	}
	end := row.StartedAt.Add(milliseconds(row.DurationMs))
	if a.first.IsZero() || row.StartedAt.Before(a.first) {
		a.first = row.StartedAt
	}
	if end.After(a.last) {
		a.last = end
	}
}

// Result is the running totals, with the figures only every statement
// decides: the average, the percentile, the window and the repeats.
func (a *Accumulator) Result() Summary {
	out := a.sum
	out.ByOp = maps.Clone(a.sum.ByOp)
	out.Tables = slices.AppendSeq(make([]string, 0, len(a.tables)), maps.Keys(a.tables))
	slices.Sort(out.Tables)
	if n := len(a.durations); n > 0 {
		out.AvgDurationMs = out.DurationMs / float64(n)
		sorted := slices.Sorted(slices.Values(a.durations))
		// The nearest-rank 95th percentile, as xetrace.Summary takes it.
		out.P95DurationMs = sorted[(n*95+99)/100-1]
	}
	if a.last.After(a.first) {
		out.WindowMs = float64(a.last.Sub(a.first)) / float64(time.Millisecond)
	}
	var repeats []Repeat
	for _, repeat := range a.repeats {
		if repeat.Count > 1 {
			out.Repeated += repeat.Count
			repeats = append(repeats, *repeat)
		}
	}
	slices.SortFunc(repeats, func(x, y Repeat) int {
		return cmp.Or(cmp.Compare(y.Count, x.Count), cmp.Compare(y.DurationMs, x.DurationMs), cmp.Compare(x.Key, y.Key))
	})
	out.Repeats = repeats[:min(len(repeats), maxRepeats)]
	return out
}

// Summarize folds rows in one call.
func Summarize(rows []StatementRow) Summary {
	a := NewAccumulator()
	for _, row := range rows {
		a.Add(row)
	}
	return a.Result()
}

// Pretty renders the summary as one line, its durations toned as an xevent
// capture's are, e.g.
//
//	5 statements · 1 failed · 419ms total · avg 83.8ms p95 146ms max 146ms · 4 rows · 3 repeated
func (s Summary) Pretty() api.Text {
	t := api.Text{}.AddText(fmt.Sprintf("%d statements", s.Statements), "font-bold")
	if s.Errors > 0 {
		t = t.AddText(" · ", "text-muted").AddText(fmt.Sprintf("%d failed", s.Errors), "text-red-500 font-bold")
	}
	if s.Statements == 0 {
		return t
	}
	total, p95, longest := milliseconds(s.DurationMs), milliseconds(s.P95DurationMs), milliseconds(s.MaxDurationMs)
	t = t.AddText(" · ", "text-muted").Add(api.Human(total, xetrace.DurationStyle(total))).AddText(" total", "text-muted").
		AddText(" · avg ", "text-muted").Add(api.Human(milliseconds(s.AvgDurationMs), "text-muted")).
		AddText(" p95 ", "text-muted").Add(api.Human(p95, xetrace.DurationStyle(p95))).
		AddText(" max ", "text-muted").Add(api.Human(longest, xetrace.DurationStyle(longest)))
	if s.Rows > 0 {
		t = t.AddText(" · ", "text-muted").Add(api.HumanNumber(s.Rows, "text-muted")).AddText(" rows", "text-muted")
	}
	if s.Repeated > 0 {
		t = t.AddText(" · ", "text-muted").AddText(fmt.Sprintf("%d repeated", s.Repeated), "text-orange-500")
	}
	return t
}

func milliseconds(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }
