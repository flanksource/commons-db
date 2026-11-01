package query

import (
	"context"
	"fmt"
	"sort"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	dbquery "github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/datetime"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/tracing"
)

// timeColumns are the row keys a read's rows are timed by, in preference order.
var timeColumns = []string{"time", "timestamp"}

// reader is one capture's read: the profile and params it executes, and the
// window it binds onto them.
type reader struct {
	profile dbquery.Profile
	params  map[string]any
	// name is the saved profile read, empty for a provider read in place.
	name string
	// windowed binds [start, end] onto each read; a zero end is the moment
	// each read runs.
	windowed   bool
	start, end time.Time
	limit      int
	// timed refuses rows that carry no time column, which a read every
	// interval needs to know what it has kept.
	timed  bool
	before profilestore.BeforeExecuteFunc
}

func (r reads) reader(ctx dbcontext.Context, params Params, now time.Time) (*reader, error) {
	profile, name, err := r.profile(ctx, params)
	if err != nil {
		return nil, err
	}
	window, err := params.window(now)
	if err != nil {
		return nil, err
	}
	read := &reader{
		profile: profile, params: params.Params, name: name, windowed: params.windowed(),
		start: window.start, end: window.end, limit: params.Limit, timed: params.Every != "", before: r.options.BeforeExecute,
	}
	if read.params == nil {
		read.params = map[string]any{}
	}
	if !read.windowed {
		return read, nil
	}
	if read.start.IsZero() {
		read.start = now
	}
	if _, _, err := dbquery.WindowProfile(profile, read.params, dbquery.ReadWindow{Start: read.start, End: read.start}); err != nil {
		return nil, err
	}
	return read, nil
}

// profile is the profile params read: the provider's in-place profile, or the
// saved one the host resolves.
func (r reads) profile(ctx context.Context, params Params) (dbquery.Profile, string, error) {
	if params.Provider != nil {
		profile, err := params.Provider.profile()
		return profile, "", err
	}
	if r.options.Profiles == nil {
		return dbquery.Profile{}, "", fmt.Errorf("profile %q cannot be read: the host serves no saved profiles", params.Profile)
	}
	profile, err := r.options.Profiles.Resolve(ctx, params.Profile)
	if err != nil {
		return dbquery.Profile{}, "", fmt.Errorf("resolve profile %q: %w", params.Profile, err)
	}
	return profile, params.Profile, nil
}

func (r *reader) label() string {
	if r.name != "" {
		return fmt.Sprintf("profile %q", r.name)
	}
	return fmt.Sprintf("provider %s", r.profile.Provider.Type)
}

// read executes the profile over [start, end] when windowed, behind the
// host's BeforeExecute, and returns its rows ordered by time when they carry one.
func (r *reader) read(ctx dbcontext.Context, start, end time.Time) ([]Row, error) {
	profile, params := r.profile, r.params
	if r.windowed {
		var err error
		if profile, params, err = dbquery.WindowProfile(r.profile, r.params, dbquery.ReadWindow{Start: start, End: end}); err != nil {
			return nil, err
		}
	}
	release, err := r.before(ctx, []profilestore.ReadRequest{{Profile: profile, Params: params}})
	if err != nil {
		return nil, fmt.Errorf("prepare the read of %s: %w", r.label(), err)
	}
	defer release()
	result, err := dbquery.Execute(ctx, profile, params)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", r.label(), err)
	}
	return r.records(result.Rows, end)
}

// records times each row by its time column, or by readAt when the read has
// none, and orders them by it.
func (r *reader) records(rows []dbquery.Row, readAt time.Time) ([]Row, error) {
	column := ""
	if len(rows) > 0 {
		for _, candidate := range timeColumns {
			if _, ok := rows[0][candidate]; ok {
				column = candidate
				break
			}
		}
	}
	if column == "" && r.timed && len(rows) > 0 {
		return nil, fmt.Errorf("%s is read every interval, but its rows carry neither time nor timestamp to tell new rows from read ones", r.label())
	}
	records := make([]Row, 0, len(rows))
	for index, row := range rows {
		at := readAt
		if column != "" {
			var err error
			if at, err = rowTime(row[column]); err != nil {
				return nil, fmt.Errorf("%s row %d %s: %w", r.label(), index, column, err)
			}
		}
		records = append(records, Row{Time: at, Profile: r.name, Provider: r.profile.Provider.Type, Row: row})
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Time.Before(records[j].Time) })
	return records, nil
}

// rowTime reads a time column's value: a time, or text in a date format.
func rowTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		parsed, err := datetime.Parse(typed, time.Time{})
		if err != nil {
			return time.Time{}, err
		}
		return parsed.Time.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%v (%T) is neither a time nor a date", value, value)
}

// kept cuts rows to the read's limit, warning the capture of what it cut.
func (r *reader) kept(rows []Row, records tracing.Emitter[Row]) []Row {
	if r.limit == 0 || len(rows) <= r.limit {
		return rows
	}
	records.Warn(fmt.Sprintf("%s returned %d rows; kept the first %d", r.label(), len(rows), r.limit))
	return rows[:r.limit]
}

// once reads the window up to end, or up to the window's own end, and emits
// every row it keeps.
func (r *reader) once(ctx dbcontext.Context, end time.Time, records tracing.Emitter[Row]) error {
	if !r.end.IsZero() {
		end = r.end
	}
	rows, err := r.read(ctx, r.start, end)
	if err != nil {
		return err
	}
	for _, row := range r.kept(rows, records) {
		if err := records.Emit(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

// follow reads [last, now] every interval, emitting only the rows timed past
// the last row it kept, until the capture stops.
func (r *reader) follow(ctx dbcontext.Context, every time.Duration, records tracing.Emitter[Row]) error {
	emitCtx := context.WithoutCancel(ctx)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	last, seen := r.start, false
	for {
		rows, err := r.read(ctx, last, time.Now().UTC())
		if err != nil && ctx.Err() == nil {
			return err
		}
		var fresh []Row
		for _, row := range rows {
			if !seen || row.Time.After(last) {
				fresh = append(fresh, row)
			}
		}
		for _, row := range r.kept(fresh, records) {
			if err := records.Emit(emitCtx, row); err != nil {
				return err
			}
			last, seen = row.Time, true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
