// Package sqlstatements is the sql_statements trace kind: it records the SQL
// statements commons-db runs on chosen connections, as the SQL statement tap
// publishes them, and summarizes them as it goes.
package sqlstatements

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

// maxValueBytes caps any one value a statement stores, such as its SQL.
const maxValueBytes = 64 << 10

// reportEvery is how often a running capture reports its Summary.
const reportEvery = time.Second

// Params choose the statements a capture records.
type Params struct {
	// Connections name the connections to observe, as profiles reference
	// them, "*" for every connection, or "self" for the server's own
	// database, which "*" does not include.
	Connections []string `json:"connections" clicky:"required,title=Connections"`
	// MinDuration, a duration such as 100ms, records only statements that ran
	// at least that long.
	MinDuration string `json:"minDuration,omitempty" clicky:"title=Minimum duration"`
}

func (p Params) Validate() error {
	if len(p.Connections) == 0 {
		return errors.New(`connections is required: name connections, "*" for every connection, or "self" for the server's own database`)
	}
	for _, name := range p.Connections {
		if strings.TrimSpace(name) == "" {
			return errors.New("connections must not name a blank connection")
		}
	}
	_, err := p.minimum()
	return err
}

func (p Params) minimum() (time.Duration, error) {
	if p.MinDuration == "" {
		return 0, nil
	}
	minimum, err := time.ParseDuration(p.MinDuration)
	if err != nil || minimum < 0 {
		return 0, fmt.Errorf("minDuration %q is not a duration such as 100ms", p.MinDuration)
	}
	return minimum, nil
}

// repeatsView serves each statement text that ran more than once as one row:
// how often, and what its runs took.
var repeatsView = recordresults.ResultView{
	Name: "repeats", Title: "Repeated SQL statements",
	Query: `SELECT repeatKey, min(sql) AS sql, count(*) AS runs,
			sum(durationMs) AS durationMs, max(durationMs) AS maxDurationMs
		FROM stream_rows GROUP BY repeatKey HAVING count(*) > 1`,
	Columns: []query.ColumnDef{
		{Name: "repeatKey", Type: query.ColumnTypeString, Hidden: true},
		{Name: "sql", Label: "Statement", Type: query.ColumnTypeString},
		{Name: "runs", Label: "Runs", Type: query.ColumnTypeNumber},
		{Name: "durationMs", Label: "Total", Type: query.ColumnTypeDuration, Unit: "ms"},
		{Name: "maxDurationMs", Label: "Longest", Type: query.ColumnTypeDuration, Unit: "ms"},
	},
	Order: query.Order{{Column: "runs", Desc: true}, {Column: "repeatKey", Unique: true}},
}

// running is what Prepare set up for Handle: the summary of the statements
// the capture stored, which the goroutines running them fold in.
type running struct {
	mu      sync.Mutex
	summary *Accumulator
}

func (r *running) add(row StatementRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summary.Add(row)
}

func (r *running) result() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summary.Result()
}

type runningKey struct{}

type statements struct{}

func (statements) Params() Params { return Params{} }

func (statements) Schema() recordresults.ResultType[StatementRow] { return ResultType() }

// ResultType is the RowKind result type: it follows a capture's stream while
// it runs, searches the text a reader remembers of a statement, or of its
// error, and serves the repeats view. A host that registers result types
// itself rather than through traces.Kinds registers this one.
func ResultType() recordresults.ResultType[StatementRow] {
	return recordresults.ResultType[StatementRow]{
		Kind: RowKind, Title: "SQL statements", TimeColumn: "startedAt", Follow: true,
		SearchColumns: []string{"sql", "error"},
		Views:         []recordresults.ResultView{repeatsView},
	}
}

// Prepare observes the connections before the session runs, emitting each
// statement without waiting: it arrives on the goroutine that ran it. The
// release stops observing and reports the Summary of every statement stored.
func (statements) Prepare(ctx dbcontext.Context, params Params, records traces.Emitter[StatementRow]) (dbcontext.Context, func() error, error) {
	minimum, err := params.minimum()
	if err != nil {
		return ctx, nil, err
	}
	capture := &running{summary: NewAccumulator()}
	deliver := func(statement connection.Statement) {
		if statement.Duration < minimum {
			return
		}
		row := FromStatement(statement)
		if records.TryEmit(row) {
			capture.add(row)
		}
	}
	var releases []func()
	for _, name := range params.Connections {
		releases = append(releases, connection.ObserveSQL(name, deliver))
	}
	return ctx.WithValue(runningKey{}, capture), func() error {
		for _, release := range releases {
			release()
		}
		records.Report(capture.result())
		return nil
	}, nil
}

// Handle reports the Summary every reportEvery until the capture stops: the
// observers Prepare installed emit every statement.
func (statements) Handle(ctx dbcontext.Context, _ Params, records traces.Emitter[StatementRow], _ traces.Records[StatementRow]) error {
	capture, ok := ctx.Value(runningKey{}).(*running)
	if !ok {
		return errors.New("sql_statements: the capture was not prepared")
	}
	ticker := time.NewTicker(reportEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			records.Report(capture.result())
		}
	}
}

// Kind is the sql_statements kind: statements masked and capped at 64KiB a
// value. A host registers it as RowKind.
func Kind() *traces.Handler[Params, StatementRow] {
	return traces.NewHandler[Params, StatementRow](statements{}, traces.Capabilities{Live: true}).
		WithSecretMasking().
		WithTruncation(maxValueBytes)
}
