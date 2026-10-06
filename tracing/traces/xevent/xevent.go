// Package xevent is the sql_xevent trace kind: a SQL Server Extended Events
// capture whose events are stored as sqltrace event rows.
package xevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/commons/logger"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing/sqltrace"
	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/flanksource/commons-db/tracing/traces"
)

// Params are the SQL Server connection to capture on and the capture's
// options, as the sqlserver-xevent provider takes them.
type Params struct {
	Connection string `json:"connection" clicky:"required,title=Connection"`
	providers.XEventCaptureOptions
}

func (p Params) Validate() error {
	if strings.TrimSpace(p.Connection) == "" {
		return errors.New("connection is required: the SQL Server connection to capture on")
	}
	_, _, err := p.CaptureOptions()
	return err
}

// Capture is how the kind reaches SQL Server: Open leases a connection's
// client, NewSession creates the XE session on it, and CurrentDatabase names
// the database a capture naming none is scoped to.
type Capture struct {
	Open            func(ctx dbcontext.Context, connection string) (*sql.DB, func(), error)
	NewSession      sqltrace.XEFactory
	CurrentDatabase func(context.Context, *sql.DB) (string, error)
}

// running is what Prepare set up for Handle: the XE session and how to drain it.
type running struct {
	session sqltrace.XESession
	name    string
	drain   xetrace.DrainOptions
}

type runningKey struct{}

type events struct{ capture Capture }

func (events) Params() Params { return Params{} }

func (events) Schema() recordresults.ResultType[sqltrace.EventRow] {
	return recordresults.ResultType[sqltrace.EventRow]{Title: "SQL Server events", TimeColumn: "timestamp"}
}

// Prepare creates the XE session before the session runs, so a connection or
// permission failure refuses the start. The release drops the XE session and
// gives the connection back; a drop that fails is the session's error.
func (e events) Prepare(ctx dbcontext.Context, params Params, _ traces.Emitter[sqltrace.EventRow]) (dbcontext.Context, func() error, error) {
	create, drain, err := params.CaptureOptions()
	if err != nil {
		return ctx, nil, err
	}
	db, releaseDB, err := e.capture.Open(ctx, params.Connection)
	if err != nil {
		return ctx, nil, err
	}
	if len(create.Databases) == 0 {
		database, err := e.capture.CurrentDatabase(ctx, db)
		if err != nil {
			releaseDB()
			return ctx, nil, err
		}
		create.Databases = []string{database}
	}
	create.Filter = create.DrainFilter()
	session, opened, err := e.capture.NewSession(ctx, db, create)
	if err != nil {
		releaseDB()
		return ctx, nil, err
	}
	drain.Filter, drain.FinalDelay = create.Filter, opened.FinalDelay
	release := func() error {
		defer releaseDB()
		if err := session.Drop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("drop XE session %q, which may still be running on the server: %w", opened.Name, err)
		}
		return nil
	}
	return ctx.WithValue(runningKey{}, &running{session: session, name: opened.Name, drain: drain}), release, nil
}

// Handle drains the XE session until the capture stops, then once more: the
// final drain's events are emitted under a context the stop does not cancel.
func (events) Handle(ctx dbcontext.Context, _ Params, records traces.Emitter[sqltrace.EventRow], _ traces.Records[sqltrace.EventRow]) error {
	capture := ctx.Value(runningKey{}).(*running)
	emitCtx := context.WithoutCancel(ctx)
	var pending []xetrace.Event
	var emitErr error
	drain := capture.drain
	drain.OnEvent = func(event xetrace.Event) { pending = append(pending, event) }
	drain.OnPollBatch = func() {
		for _, event := range pending {
			if err := records.Emit(emitCtx, sqltrace.FromEvent(event)); err != nil && emitErr == nil {
				emitErr = err
			}
		}
		pending = nil
	}
	drain.OnPollFailure = func(consecutive int, err error) {
		logger.Warnf("sql_xevent: poll %d of session %q failed, retrying: %v", consecutive, capture.name, err)
	}
	drain.OnDropped = func(delta int64, stats xetrace.TargetStats) {
		logger.Warnf("sql_xevent: session %q lost %d event(s) before they could be read (truncated=%v)",
			capture.name, delta, stats.Truncated)
	}
	return errors.Join(xetrace.Drain(ctx, capture.session, drain), emitErr)
}

// NewKind is the sql_xevent kind over capture: events masked, but never the
// session ids and users that only look sensitive.
func NewKind(capture Capture) *traces.Handler[Params, sqltrace.EventRow] {
	return traces.NewHandler[Params, sqltrace.EventRow](events{capture: capture}, traces.Capabilities{Live: true}).
		WithSecretMasking("sessionId", "username")
}

// Kind is the sql_xevent kind against real SQL Server connections.
func Kind() *traces.Handler[Params, sqltrace.EventRow] {
	return NewKind(Capture{
		Open:            providers.OpenSQLServer,
		NewSession:      sqltrace.CreateXESession,
		CurrentDatabase: sqltrace.CurrentDatabase,
	})
}
