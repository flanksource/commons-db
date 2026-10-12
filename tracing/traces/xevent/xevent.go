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
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing/sqltrace"
	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/flanksource/commons-db/tracing/traces"
)

// Options are what a capture opens: the connection it captures on, the XE
// session it creates there, and how that session is drained.
type Options struct {
	Connection string
	Create     xetrace.CreateOptions
	Drain      xetrace.DrainOptions
}

// Capturable is a params type that maps itself onto the capture it opens. It
// is asked once as the capture starts, so options it takes from the clock,
// such as a drain's StartedAt, are the start's.
type Capturable interface {
	XEventOptions() (Options, error)
}

// Params are the SQL Server connection to capture on and the capture's
// options, as the sqlserver-xevent provider takes them.
type Params struct {
	Connection string `json:"connection" clicky:"required,title=Connection"`
	providers.XEventCaptureOptions
}

func (p Params) XEventOptions() (Options, error) {
	if strings.TrimSpace(p.Connection) == "" {
		return Options{}, errors.New("connection is required: the SQL Server connection to capture on")
	}
	create, drain, err := p.CaptureOptions()
	return Options{Connection: p.Connection, Create: create, Drain: drain}, err
}

func (p Params) Validate() error {
	_, err := p.XEventOptions()
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

// running is what Prepare set up for Handle: the XE session, how to drain it,
// and the connection its deadlock reports are resolved through.
type running struct {
	session sqltrace.XESession
	name    string
	drain   xetrace.DrainOptions
	db      *sql.DB
}

type runningKey struct{}

type events[P Capturable] struct {
	capture Capture
}

// Params start a capture from P's zero value; a host prefills its own form
// with Handler.WithDefaults.
func (events[P]) Params() P {
	var defaults P
	return defaults
}

// Schema follows a capture's stream while it runs, and searches the text a
// reader remembers of a statement rather than a column.
func (events[P]) Schema() recordresults.ResultType[sqltrace.EventRow] {
	return recordresults.ResultType[sqltrace.EventRow]{
		Title: "SQL Server events", TimeColumn: "timestamp", Follow: true,
		SearchColumns: []string{"sql", "rawStatement", "objectName", "errorMessage"},
	}
}

// Prepare creates the XE session before the session runs, so a connection or
// permission failure refuses the start, and names the session after it. The
// release drops the XE session and gives the connection back; a drop that
// fails is the session's error.
func (e events[P]) Prepare(ctx dbcontext.Context, params P, records traces.Emitter[sqltrace.EventRow]) (dbcontext.Context, func() error, error) {
	options, err := params.XEventOptions()
	if err != nil {
		return ctx, nil, err
	}
	db, releaseDB, err := e.capture.Open(ctx, options.Connection)
	if err != nil {
		return ctx, nil, err
	}
	create, drain := options.Create, options.Drain
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
	records.Describe(opened.Name, sessionMetadata(create.Databases, opened.Statements)...)
	release := func() error {
		defer releaseDB()
		if err := session.Drop(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("drop XE session %q, which may still be running on the server: %w", opened.Name, err)
		}
		return nil
	}
	return ctx.WithValue(runningKey{}, &running{session: session, name: opened.Name, drain: drain, db: db}), release, nil
}

// sessionMetadata is the databases the capture is scoped to and the script
// that opened the XE session, each statement ;-terminated: where the capture's
// filters and target sizing actually land.
func sessionMetadata(databases, statements []string) []query.SessionMetadata {
	metadata := []query.SessionMetadata{{
		Name: "sql_xevent.databases", Label: "Databases", Value: strings.Join(databases, ", "),
	}}
	if len(statements) == 0 {
		return metadata
	}
	return append(metadata, query.SessionMetadata{
		Name: "sql_xevent.statements", Label: "Started with", Language: "sql",
		Value: strings.Join(statements, ";\n\n") + ";",
	})
}

// Handle drains the XE session until the capture stops, then once more: the
// final drain's events are emitted under a context the stop does not cancel.
// Each poll's events are decoded, deadlock reports included, and folded into
// the xetrace.Summary the capture reports; events the session lost are counted
// there and end the session with a warning.
func (events[P]) Handle(ctx dbcontext.Context, _ P, records traces.Emitter[sqltrace.EventRow], _ traces.Records[sqltrace.EventRow]) error {
	capture := ctx.Value(runningKey{}).(*running)
	emitCtx := context.WithoutCancel(ctx)
	summary := xetrace.NewAccumulator()
	var pending []xetrace.Event
	var emitErr error
	drain := capture.drain
	drain.OnEvent = func(event xetrace.Event) { pending = append(pending, event) }
	drain.OnUnresolved = func(xetrace.Event) { summary.AddUnresolved() }
	drain.OnPollBatch = func() {
		for _, event := range pending {
			if err := records.Emit(emitCtx, sqltrace.DecodeEvent(emitCtx, capture.db, event)); err != nil && emitErr == nil {
				emitErr = err
			}
		}
		summary.AddAll(pending)
		pending = nil
		records.Report(summary.Result())
	}
	drain.OnPollFailure = func(consecutive int, err error) {
		logger.Warnf("sql_xevent: poll %d of session %q failed, retrying: %v", consecutive, capture.name, err)
	}
	drain.OnDropped = func(delta int64, stats xetrace.TargetStats) {
		summary.AddLost(delta)
		logger.Warnf("sql_xevent: session %q lost %d event(s) before they could be read (truncated=%v)",
			capture.name, delta, stats.Truncated)
	}
	err := xetrace.Drain(ctx, capture.session, drain)
	result := summary.Result()
	records.Report(result)
	if result.Lost > 0 {
		records.Warn(fmt.Sprintf("lost %d event(s) before they could be read: raise minDuration, shorten poll, or capture to an event file", result.Lost))
	}
	return errors.Join(err, emitErr)
}

// NewKindFor is the sql_xevent kind over capture for a host's own params
// type: events masked, but never the session ids and users that only look
// sensitive.
func NewKindFor[P Capturable](capture Capture) *traces.Handler[P, sqltrace.EventRow] {
	return traces.NewHandler[P, sqltrace.EventRow](events[P]{capture: capture}, traces.Capabilities{Live: true}).
		WithSecretMasking("sessionId", "username")
}

// NewKind is the sql_xevent kind over capture.
func NewKind(capture Capture) *traces.Handler[Params, sqltrace.EventRow] {
	return NewKindFor[Params](capture)
}

// Kind is the sql_xevent kind against real SQL Server connections.
func Kind() *traces.Handler[Params, sqltrace.EventRow] {
	return NewKind(Capture{
		Open:            providers.OpenSQLServer,
		NewSession:      sqltrace.CreateXESession,
		CurrentDatabase: sqltrace.CurrentDatabase,
	})
}
