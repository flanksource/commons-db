package providers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
)

type SQLPageRequest struct {
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

	started := time.Now()
	// Gated on WantsPreview rather than on the sink existing: a reconciliation
	// records itself on every run, and turning on server debug logging for every
	// page of an ordinary read is a debug-run price an ordinary read must not pay.
	queryContext, queryID, providerDetails := clickHouseDiagnosticContext(
		ctx,
		dialect == dialectClickHouse && request.Diagnostics.WantsPreview(),
	)
	defer func() {
		if request.Diagnostics == nil {
			return
		}
		details := map[string]any{
			"dialect": string(dialect),
			"paging": map[string]any{
				"limit": request.Page.Limit, "offset": request.Page.Offset,
				"mode": request.Page.Mode().String(), "hasMore": result.HasMore,
			},
		}
		if queryID != "" {
			details["clickhouse"] = providerDetails()
		}
		if result.Total != nil {
			details["total"] = map[string]any{"value": result.Total.Value, "relation": result.Total.Relation()}
		}
		if err != nil {
			request.Diagnostics.RecordError(err)
		}
		// Marshalling every page's rows costs more than the preview is worth
		// outside a debug run, and RecordPreview would discard it anyway.
		if len(result.Rows) > 0 && request.Diagnostics.WantsPreview() {
			request.Diagnostics.RecordPreview("application/json", query.MarshalDiagnosticPreview(result.Rows))
		}
		request.Diagnostics.RecordResponse(started, len(result.Rows), details)
	}()

	statement, args, err := buildPagedSQL(
		dialect,
		request.Query,
		request.Filters,
		request.Order,
		request.Position,
		request.Page,
	)
	if err != nil {
		return SQLPageResult{}, err
	}
	statement, err = materializeSQLParams(dialect, statement, request.QueryArgs, request.QueryIdentifiers)
	if err != nil {
		return SQLPageResult{}, err
	}
	args = append(append([]any{}, request.QueryArgs...), args...)
	requestDetails := map[string]any{
		"dialect": string(dialect),
		"paging": map[string]any{
			"limit": request.Page.Limit, "offset": request.Page.Offset, "mode": request.Page.Mode().String(),
		},
	}
	if queryID != "" {
		requestDetails["queryId"] = queryID
	}
	request.Diagnostics.RecordRequest(statement, args, requestDetails)

	// A failure is the one moment the statement that ran is worth more than it
	// costs to carry, so it travels on the error rather than only in a debug
	// run's diagnostics — "column does not exist" names no column and no query.
	// A debug run already hands its own diagnostics back to the caller.
	defer func() {
		if err == nil || request.Diagnostics != nil {
			return
		}
		failure := query.NewDiagnostics(query.DiagnosticOptions{Provider: string(dialect), Query: request.Query, Detail: query.DiagnosticFull})
		failure.RecordRequest(statement, args, requestDetails)
		err = query.WithDiagnostics(err, failure)
	}()

	rows, err := client.QueryContext(queryContext, statement, args...)
	if err != nil {
		return SQLPageResult{}, sqlExecError(dialect, request.Filters, err)
	}
	defer func() { _ = rows.Close() }()
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
