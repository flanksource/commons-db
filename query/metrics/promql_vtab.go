package metrics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons/duration"
	"modernc.org/sqlite/vtab"
)

// Range is a PromQL range-query window.
type Range struct {
	Start, End time.Time
	Step       time.Duration
}

// RangeQuery lets metric SQL read `prometheus(query, [start, end, step])`.
// Window is the provider's request range, the default for omitted bounds;
// Fetch evaluates one expression over a range, emitting each sample.
type RangeQuery struct {
	Window Range
	Fetch  func(expr string, r Range, emit func(Sample) error) error
}

func init() {
	if err := vtab.RegisterModule(nil, "cdb_prometheus", promModule{}); err != nil {
		panic(err)
	}
}

// vtab.RegisterModule is process-global, so each Execute binds its range
// query under its own key, passed as the CREATE VIRTUAL TABLE argument.
var (
	rangeQueries   sync.Map
	rangeQueryKeys atomic.Int64
)

func attachPrometheus(ctx context.Context, db *sql.DB, rq *RangeQuery) (func(), error) {
	key := fmt.Sprintf("range_%d", rangeQueryKeys.Add(1))
	rangeQueries.Store(key, rq)
	release := func() { rangeQueries.Delete(key) }
	if _, err := db.ExecContext(ctx, "CREATE VIRTUAL TABLE temp.prometheus USING cdb_prometheus("+key+")"); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

const (
	promColQuery = 5 + iota
	promColStart
	promColEnd
	promColStep
)

type promModule struct{}

func (promModule) Create(ctx vtab.Context, args []string) (vtab.Table, error) {
	if len(args) < 4 {
		return nil, fmt.Errorf("cdb_prometheus requires a range query key")
	}
	key := strings.Trim(strings.TrimSpace(args[3]), `'"`)
	rq, ok := rangeQueries.Load(key)
	if !ok {
		return nil, fmt.Errorf("cdb_prometheus range query %q is not bound", key)
	}
	table := &promTable{rq: rq.(*RangeQuery), cache: map[string][]promRow{}}
	return table, ctx.Declare("CREATE TABLE x(time TEXT, value REAL, series_id TEXT, metric_name TEXT, labels TEXT, query HIDDEN, start HIDDEN, end HIDDEN, step HIDDEN)")
}
func (m promModule) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Create(ctx, args)
}

type promRow struct {
	time      string
	value     float64
	id, name  string
	labels    string
	arguments [4]vtab.Value
}

// promTable memoises each (query, range) it fetched: SQLite rescans the inner
// side of a join once per outer row, and each scan must not re-query.
type promTable struct {
	rq    *RangeQuery
	mu    sync.Mutex
	cache map[string][]promRow
}

func (t *promTable) BestIndex(info *vtab.IndexInfo) error {
	constraint := [4]int{-1, -1, -1, -1}
	for i, c := range info.Constraints {
		if c.Column >= promColQuery && c.Column <= promColStep && c.Op == vtab.OpEQ && c.Usable && constraint[c.Column-promColQuery] < 0 {
			constraint[c.Column-promColQuery] = i
		}
	}
	if constraint[0] < 0 {
		return fmt.Errorf("prometheus requires a query: SELECT ... FROM prometheus('<promql>'[, start, end, step])")
	}
	mask, arg := int64(0), 0
	for slot, i := range constraint {
		if i < 0 {
			continue
		}
		info.Constraints[i].ArgIndex = arg
		info.Constraints[i].Omit = true
		mask |= 1 << slot
		arg++
	}
	info.IdxNum = mask
	info.EstimatedRows = 1000
	info.EstimatedCost = 1000
	return nil
}
func (t *promTable) Open() (vtab.Cursor, error) { return &promCursor{table: t}, nil }
func (t *promTable) Disconnect() error          { return nil }
func (t *promTable) Destroy() error             { return nil }

func (t *promTable) rows(expr string, r Range) ([]promRow, error) {
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%d", expr, r.Start.UnixNano(), r.End.UnixNano(), r.Step)
	t.mu.Lock()
	defer t.mu.Unlock()
	if rows, ok := t.cache[key]; ok {
		return rows, nil
	}
	arguments := [4]vtab.Value{expr, formatTime(r.Start), formatTime(r.End), r.Step.String()}
	rows := []promRow{}
	err := t.rq.Fetch(expr, r, func(s Sample) error {
		row, err := newPromRow(s, arguments)
		rows = append(rows, row)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("prometheus(%q): %w", expr, err)
	}
	t.cache[key] = rows
	return rows, nil
}

func newPromRow(s Sample, arguments [4]vtab.Value) (promRow, error) {
	labels := make(map[string]string, len(s.Labels))
	for k, v := range s.Labels {
		if k != "__name__" {
			labels[k] = v
		}
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return promRow{}, err
	}
	name := s.Labels["__name__"]
	id := fmt.Sprintf("%x", sha256.Sum256(append([]byte(name+"\x00"), encoded...)))
	return promRow{time: formatTime(s.Time), value: s.Value, id: id, name: name, labels: string(encoded), arguments: arguments}, nil
}

type promCursor struct {
	table *promTable
	rows  []promRow
	index int
}

func (c *promCursor) Filter(idxNum int, _ string, values []vtab.Value) error {
	r, expr, err := c.table.rangeOf(int64(idxNum), values)
	if err != nil {
		return err
	}
	c.rows, err = c.table.rows(expr, r)
	c.index = 0
	return err
}

// rangeOf reads the hidden-column arguments BestIndex selected (bit n of
// mask = argument n present), defaulting the bounds to the request window.
func (t *promTable) rangeOf(mask int64, values []vtab.Value) (Range, string, error) {
	texts := [4]string{}
	next := 0
	for slot := range texts {
		if mask&(1<<slot) == 0 {
			continue
		}
		if next >= len(values) {
			return Range{}, "", fmt.Errorf("prometheus received %d arguments, expected more", len(values))
		}
		text, ok := values[next].(string)
		if !ok {
			return Range{}, "", fmt.Errorf("prometheus arguments must be strings, got %T", values[next])
		}
		texts[slot] = text
		next++
	}
	r := t.rq.Window
	if err := parseBound(texts[1], &r.Start); err != nil {
		return Range{}, "", fmt.Errorf("prometheus start: %w", err)
	}
	if err := parseBound(texts[2], &r.End); err != nil {
		return Range{}, "", fmt.Errorf("prometheus end: %w", err)
	}
	if texts[3] != "" {
		d, err := duration.ParseDuration(texts[3])
		if err != nil {
			return Range{}, "", fmt.Errorf("prometheus step: %w", err)
		}
		r.Step = time.Duration(d)
	}
	if strings.TrimSpace(texts[0]) == "" {
		return Range{}, "", fmt.Errorf("prometheus requires a non-empty query")
	}
	if r.Step <= 0 || r.End.Before(r.Start) {
		return Range{}, "", fmt.Errorf("prometheus range %s..%s step %s is invalid", r.Start, r.End, r.Step)
	}
	return r, texts[0], nil
}

func parseBound(text string, into *time.Time) error {
	if text == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return err
	}
	*into = t
	return nil
}

func (c *promCursor) Next() error { c.index++; return nil }
func (c *promCursor) Eof() bool   { return c.index >= len(c.rows) }
func (c *promCursor) Column(col int) (vtab.Value, error) {
	row := c.rows[c.index]
	switch col {
	case 0:
		return row.time, nil
	case 1:
		return row.value, nil
	case 2:
		return row.id, nil
	case 3:
		return row.name, nil
	case 4:
		return row.labels, nil
	case promColQuery, promColStart, promColEnd, promColStep:
		return row.arguments[col-promColQuery], nil
	}
	return nil, fmt.Errorf("invalid prometheus column %d", col)
}
func (c *promCursor) Rowid() (int64, error) { return int64(c.index), nil }
func (c *promCursor) Close() error          { return nil }
