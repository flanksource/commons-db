// Package sqlstatements is the sql trace kind: it records the SQL statements
// commons-db runs on chosen connections, as the SQL statement tap publishes them.
package sqlstatements

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

// maxValueBytes caps any one value a statement stores, such as its SQL.
const maxValueBytes = 64 << 10

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

// StatementRow is one statement as a capture stores it.
type StatementRow struct {
	traces.Truncation
	StartedAt  time.Time `json:"startedAt" pretty:"label=Started" sort:"startedAt"`
	Connection string    `json:"connection" filter:"terms"`
	Driver     string    `json:"driver" filter:"terms"`
	SQL        string    `json:"sql" pretty:"label=SQL" filter:"text"`
	Args       []any     `json:"args,omitempty" pretty:"hide"`
	DurationMs float64   `json:"durationMs" pretty:"label=Duration,type=duration,unit=ms" sort:"durationMs"`
	Rows       int64     `json:"rows" sort:"rows"`
	Error      string    `json:"error,omitempty" filter:"text"`
}

// FromStatement is the row a capture stores for statement.
func FromStatement(statement connection.Statement) StatementRow {
	return StatementRow{
		StartedAt: statement.StartedAt.UTC(), Connection: statement.Connection, Driver: statement.Driver,
		SQL: statement.SQL, Args: statement.Args, DurationMs: float64(statement.Duration.Microseconds()) / 1000,
		Rows: statement.Rows, Error: statement.Error,
	}
}

type statements struct{}

func (statements) Params() Params { return Params{} }

func (statements) Schema() recordresults.ResultType[StatementRow] {
	return recordresults.ResultType[StatementRow]{Title: "SQL statements", TimeColumn: "startedAt"}
}

// Prepare observes the connections before the session runs, emitting each
// statement without waiting: it arrives on the goroutine that ran it.
func (statements) Prepare(ctx dbcontext.Context, params Params, records traces.Emitter[StatementRow]) (dbcontext.Context, func() error, error) {
	minimum, err := params.minimum()
	if err != nil {
		return ctx, nil, err
	}
	deliver := func(statement connection.Statement) {
		if statement.Duration >= minimum {
			records.TryEmit(FromStatement(statement))
		}
	}
	var releases []func()
	for _, name := range params.Connections {
		releases = append(releases, connection.ObserveSQL(name, deliver))
	}
	return ctx, func() error {
		for _, release := range releases {
			release()
		}
		return nil
	}, nil
}

// Handle waits for the capture to stop: the observers Prepare installed emit
// every statement.
func (statements) Handle(ctx dbcontext.Context, _ Params, _ traces.Emitter[StatementRow], _ traces.Records[StatementRow]) error {
	<-ctx.Done()
	return nil
}

// Kind is the sql trace kind: statements masked and capped at 64KiB a value.
func Kind() *traces.Handler[Params, StatementRow] {
	return traces.NewHandler[Params, StatementRow](statements{}, traces.Capabilities{Live: true}).
		WithSecretMasking().
		WithTruncation(maxValueBytes)
}
