package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons/duration"
	"modernc.org/sqlite/vtab"
)

func init() {
	if err := vtab.RegisterModule(nil, "cdb_time_series", seriesModule{}); err != nil {
		panic(err)
	}
}
// InitializeWorkspace prepares an owned writable database for metric queries.
func InitializeWorkspace(ctx context.Context, db *sql.DB) error {
	db.SetMaxOpenConns(1)
	_, err := db.ExecContext(ctx, "CREATE VIRTUAL TABLE IF NOT EXISTS temp.time_series USING cdb_time_series")
	return err
}

type seriesModule struct{}

func (seriesModule) Create(ctx vtab.Context, _ []string) (vtab.Table, error) {
	return seriesTable{}, ctx.Declare("CREATE TABLE x(time TEXT, start HIDDEN, end HIDDEN, step HIDDEN)")
}
func (m seriesModule) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Create(ctx, args)
}

type seriesTable struct{}

func (seriesTable) BestIndex(info *vtab.IndexInfo) error {
	found := [3]bool{}
	for i, c := range info.Constraints {
		if c.Column >= 1 && c.Column <= 3 && c.Op == vtab.OpEQ && c.Usable {
			info.Constraints[i].ArgIndex = c.Column - 1
			info.Constraints[i].Omit = true
			found[c.Column-1] = true
		}
	}
	if !found[0] || !found[1] || !found[2] {
		return fmt.Errorf("time_series requires start, end, step")
	}
	info.EstimatedRows = 1000
	info.EstimatedCost = 1000
	return nil
}
func (seriesTable) Open() (vtab.Cursor, error) { return &seriesCursor{}, nil }
func (seriesTable) Disconnect() error          { return nil }
func (seriesTable) Destroy() error             { return nil }

type seriesCursor struct {
	current, end time.Time
	step         time.Duration
	row          int64
	args         []vtab.Value
}

func (c *seriesCursor) Filter(_ int, _ string, values []vtab.Value) error {
	if len(values) != 3 {
		return fmt.Errorf("time_series requires start, end, step")
	}
	texts := [3]string{}
	for i, v := range values {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("time_series arguments must be strings")
		}
		texts[i] = s
	}
	var err error
	c.current, err = time.Parse(time.RFC3339Nano, texts[0])
	if err != nil {
		return err
	}
	c.end, err = time.Parse(time.RFC3339Nano, texts[1])
	if err != nil {
		return err
	}
	d, err := duration.ParseDuration(texts[2])
	if err != nil || d <= 0 {
		return fmt.Errorf("time_series step must be positive")
	}
	c.step = time.Duration(d)
	c.row = 0
	c.args = append([]vtab.Value(nil), values...)
	return nil
}
func (c *seriesCursor) Next() error {
	next := c.current.Add(c.step)
	if !next.After(c.current) {
		return fmt.Errorf("time_series timestamp overflow")
	}
	c.current = next
	c.row++
	return nil
}
func (c *seriesCursor) Eof() bool { return !c.current.Before(c.end) }
func (c *seriesCursor) Column(col int) (vtab.Value, error) {
	if col == 0 {
		return formatTime(c.current), nil
	}
	if col >= 1 && col <= 3 {
		return c.args[col-1], nil
	}
	return nil, fmt.Errorf("invalid time_series column")
}
func (c *seriesCursor) Rowid() (int64, error) { return c.row, nil }
func (c *seriesCursor) Close() error          { return nil }
