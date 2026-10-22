package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
)

type SQLPageRequest struct {
	// Connection names the connection client is, for the SQL statement tap.
	Connection       string
	Query            string
	QueryArgs        []any
	QueryIdentifiers []string
	Filters          []query.ColumnFilterValue
	Order            query.Order
	Position         query.CursorPosition
	Page             query.PageRequest
	Diagnostics      *query.ProviderDiagnostics
}

type SQLPageResult struct {
	Rows        []query.Row
	ColumnTypes []*sql.ColumnType
	Total       *query.Total
	HasMore     bool
}

func ReadSQLPage(ctx context.Context, client *sql.DB, driver string, request SQLPageRequest) (result SQLPageResult, err error) {
	dialect := sqlDialect(driver)
	switch dialect {
	case dialectPostgres, dialectMySQL, dialectSQLServer, dialectClickHouse, dialectSQLite:
	default:
		return SQLPageResult{}, fmt.Errorf("unsupported sql connection type %q", driver)
	}
	if err := request.Page.Validate(); err != nil {
		return SQLPageResult{}, err
	}

	trace := sqlPageTrace{request: request, dialect: dialect, started: time.Now()}
	// Gated on WantsPreview rather than on the sink existing: a reconciliation
	// records itself on every run, and turning on server debug logging for every
	// page of an ordinary read is a debug-run price an ordinary read must not pay.
	queryContext, queryID, providerDetails := clickHouseDiagnosticContext(
		ctx,
		dialect == dialectClickHouse && request.Diagnostics.WantsPreview(),
	)
	trace.queryID, trace.providerDetails = queryID, providerDetails
	defer func() { trace.recordResponse(result, err) }()

	built, err := buildPagedSQL(dialect, request)
	if err != nil {
		return SQLPageResult{}, err
	}
	statement, args, err := built.render()
	if err != nil {
		return SQLPageResult{}, err
	}
	run := sqlStatementRun{
		Connection: request.Connection, Dialect: dialect, Statement: statement, Args: args, Started: trace.started,
	}
	requestDetails := trace.requestDetails()
	request.Diagnostics.RecordRequest(statement, args, requestDetails)

	defer func() { publishSQLStatement(run, len(result.Rows), err) }()
	defer func() { err = withSQLFailure(err, request, run, requestDetails) }()

	// The statement is the dialect generator's output for a tree: the operator's
	// own SQL parsed (the connection browser gates it on
	// readOnlyStatementError), wrapped by nodes, with every request-supplied
	// value a bind node whose value travels in args and every identifier an
	// Identifier node the generator quotes.
	rows, err := client.QueryContext(queryContext, statement, driverArgs(dialect, args)...)
	if err != nil {
		return SQLPageResult{}, sqlExecError(dialect, request.Filters, err)
	}
	defer func() { _ = rows.Close() }()
	return scanSQLPage(rows, request)
}

// sqlPageTrace records one page read into the request's diagnostics.
type sqlPageTrace struct {
	request         SQLPageRequest
	dialect         sqlDialect
	started         time.Time
	queryID         string
	providerDetails func() map[string]any
}

func (t sqlPageTrace) requestDetails() map[string]any {
	page := t.request.Page
	details := map[string]any{
		"dialect": string(t.dialect),
		"paging": map[string]any{
			"limit": page.Limit, "offset": page.Offset, "mode": page.Mode().String(),
		},
	}
	if t.queryID != "" {
		details["queryId"] = t.queryID
	}
	return details
}

func (t sqlPageTrace) recordResponse(result SQLPageResult, err error) {
	diagnostics, page := t.request.Diagnostics, t.request.Page
	if diagnostics == nil {
		return
	}
	details := map[string]any{
		"dialect": string(t.dialect),
		"paging": map[string]any{
			"limit": page.Limit, "offset": page.Offset,
			"mode": page.Mode().String(), "hasMore": result.HasMore,
		},
	}
	if t.queryID != "" {
		details["clickhouse"] = t.providerDetails()
	}
	if result.Total != nil {
		details["total"] = map[string]any{"value": result.Total.Value, "relation": result.Total.Relation()}
	}
	if err != nil {
		diagnostics.RecordError(err)
	}
	// Marshalling every page's rows costs more than the preview is worth
	// outside a debug run, and RecordPreview would discard it anyway.
	if len(result.Rows) > 0 && diagnostics.WantsPreview() {
		diagnostics.RecordPreview("application/json", query.MarshalDiagnosticPreview(result.Rows))
	}
	diagnostics.RecordResponse(t.started, len(result.Rows), details)
}

// withSQLFailure attaches the statement that ran to a failure. A failure is the
// one moment that statement is worth more than it costs to carry, so it travels
// on the error rather than only in a debug run's diagnostics — "column does not
// exist" names no column and no query. A debug run already hands its own
// diagnostics back to the caller.
func withSQLFailure(err error, request SQLPageRequest, run sqlStatementRun, details map[string]any) error {
	if err == nil || request.Diagnostics != nil {
		return err
	}
	failure := query.NewDiagnostics(query.DiagnosticOptions{Provider: string(run.Dialect), Query: request.Query, Detail: query.DiagnosticFull})
	failure.RecordRequest(run.Statement, run.Args, details)
	return query.WithDiagnostics(err, failure)
}

func scanSQLPage(rows *sql.Rows, request SQLPageRequest) (result SQLPageResult, err error) {
	result.ColumnTypes, err = rows.ColumnTypes()
	if err != nil {
		return SQLPageResult{}, fmt.Errorf("failed to describe sql rows: %w", err)
	}
	scanner, err := db.NewRowScanner(rows)
	if err != nil {
		return SQLPageResult{}, fmt.Errorf("failed to prepare sql rows: %w", err)
	}
	result.Rows = make([]query.Row, 0, request.Page.Limit+1)
	for len(result.Rows) <= request.Page.Limit && scanner.Next() {
		row := query.Row(scanner.Row())
		counted, found, err := takeRowTotal(row)
		if err != nil {
			return SQLPageResult{}, err
		}
		if found {
			result.Total = &query.Total{Value: counted, Exact: true}
		}
		result.Rows = append(result.Rows, row)
	}
	if err := scanner.Err(); err != nil {
		return SQLPageResult{}, fmt.Errorf("failed to read sql rows: %w", err)
	}
	result.HasMore = len(result.Rows) > request.Page.Limit
	if result.HasMore {
		result.Rows = result.Rows[:request.Page.Limit]
	}
	if !request.Page.SkipTotal && result.Total == nil && request.Page.Offset == 0 && request.Position.IsZero() {
		result.Total = &query.Total{Exact: true}
	}
	return result, nil
}

// sqlStatementRun is one statement as it ran on the named connection.
type sqlStatementRun struct {
	Connection string
	Dialect    sqlDialect
	Statement  string
	Args       []any
	Started    time.Time
}

// publishSQLStatement hands a statement that ran, with the rows it returned or
// the error it failed with, to the SQL statement tap.
func publishSQLStatement(run sqlStatementRun, rows int, err error) {
	if !connection.ObservingSQL(run.Connection) {
		return
	}
	published := connection.Statement{
		Connection: run.Connection, Driver: string(run.Dialect), SQL: run.Statement, Args: run.Args,
		StartedAt: run.Started, Duration: time.Since(run.Started), Rows: int64(rows),
	}
	if err != nil {
		published.Error = err.Error()
	}
	connection.PublishSQL(published)
}
