// Package providers contains the built-in data providers for the query engine.
// Each provider self-registers via init(); consumers enable them with a blank
// import:
//
//	import _ "github.com/flanksource/commons-db/query/providers"
package providers

import (
	"database/sql"
	"fmt"
	"iter"
	"slices"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

func init() {
	// The generic "sql" provider takes the driver from options/connection; the
	// per-engine aliases preset it so `provider.type: clickhouse` works directly.
	query.RegisterProvider(sqlProvider{key: "sql"})
	query.RegisterProvider(sqlProvider{key: "postgres", connType: models.ConnectionTypePostgres})
	query.RegisterProvider(sqlProvider{key: "mysql", connType: models.ConnectionTypeMySQL})
	query.RegisterProvider(sqlProvider{key: "sqlserver", connType: models.ConnectionTypeSQLServer})
	query.RegisterProvider(sqlProvider{key: "clickhouse", connType: models.ConnectionTypeClickHouse})
}

// sqlProvider runs arbitrary SQL against a postgres, mysql, sqlserver, or
// clickhouse connection and returns the rows as generic records.
type sqlProvider struct {
	// key is the registry type this instance is registered under.
	key string
	// connType forces the connection driver type; empty means take it from options.
	connType string
}

func (p sqlProvider) Type() string { return p.key }

// sqlOptions are the provider-specific knobs decoded from ProviderRequest.Options.
type sqlOptions struct {
	// Type overrides the connection driver type (postgres, mysql, sql_server, clickhouse).
	Type string `json:"type,omitempty"`

	// URL is an inline DSN used when no stored connection is referenced.
	URL string `json:"url,omitempty"`

	// Database overrides the database from the hydrated connection URL.
	Database string `json:"database,omitempty"`
}

// PagingModes reports both strategies, but they are not equally good and the
// difference is the author's to close. Offset is always available and always
// costs a row read per row skipped, because the statement belongs to whoever
// wrote it and is never rewritten to carry a LIMIT/OFFSET. Cursor paging is
// available to a profile that declares a cursor-role param and writes its own
// resume predicate against its declared order.
func (p sqlProvider) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (p sqlProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	var result []query.Row
	for page, err := range p.Pages(ctx, req, query.PageRequest{Limit: query.DefaultMaxPageSize}) {
		if err != nil {
			return nil, err
		}
		result = append(result, page.Rows...)
	}
	return result, nil
}

// Pages reads the author's statement once and hands back consecutive batches of
// it. Ending the range closes the driver cursor and the connection, so a caller
// taking a single page does not hold either.
func (p sqlProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		keyed, err := sqlCursorWiring(req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		rows, client, scanner, err := p.open(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		defer func() {
			_ = rows.Close()
			_ = client.Close()
		}()

		// Offset is served by reading and discarding. The statement is the
		// author's, so there is nowhere to put an OFFSET the backend could
		// honour — which is the whole cost that a keyset cursor avoids.
		for skipped := 0; skipped < page.Offset; skipped++ {
			if !scanner.Next() {
				if err := scanner.Err(); err != nil {
					yield(query.Page{}, fmt.Errorf("failed to read sql rows: %w", err))
					return
				}
				yield(query.Page{}, nil)
				return
			}
		}

		var carry []query.Row
		for {
			// One row past the page proves there is another page, without
			// inferring it from a batch that merely came up short.
			batch := carry
			carry = nil
			for len(batch) <= page.Limit && scanner.Next() {
				batch = append(batch, query.Row(scanner.Row()))
			}
			if err := scanner.Err(); err != nil {
				yield(query.Page{}, fmt.Errorf("failed to read sql rows: %w", err))
				return
			}

			more := len(batch) > page.Limit
			if more {
				carry = append([]query.Row(nil), batch[page.Limit:]...)
				batch = batch[:page.Limit]
			}

			current := query.Page{Rows: batch, HasMore: more}
			if keyed && len(batch) > 0 {
				keys, err := orderKeys(batch[len(batch)-1], req.Order)
				if err != nil {
					yield(query.Page{}, err)
					return
				}
				current.NextKeys = keys
			}
			if !yield(current, nil) || !more {
				return
			}
		}
	}
}

// sqlCursorWiring reports whether this profile can be cursored, and refuses a
// position it would otherwise ignore.
//
// A cursor reaches a SQL backend only through the author's own resume
// predicate. If a position arrives at a query that never templated it, the
// query returns its first page again — and paging that repeats page one
// forever is the kind of wrong that reads as working.
func sqlCursorWiring(req query.ProviderRequest) (bool, error) {
	var name string
	for param, role := range req.ParamRoles {
		if role == query.ParamRoleCursor {
			name = param
			break
		}
	}
	templated := name != "" && slices.Contains(req.TemplatedParams, name)
	if !req.Position.IsZero() && !templated {
		if name == "" {
			return false, fmt.Errorf(
				"this profile has no parameter with `role: cursor`, so a cursor cannot reach its query; declare one and reference it in a resume predicate")
		}
		return false, fmt.Errorf(
			"parameter %q has `role: cursor` but the query never references it, so the cursor would be ignored; add a resume predicate such as {{.params.%s.<column>}}",
			name, name)
	}
	return templated && len(req.Order) > 0, nil
}

// orderKeys reads a row's position in the declared order, which is what the
// next page resumes after.
func orderKeys(row query.Row, order query.Order) ([]any, error) {
	keys := make([]any, 0, len(order))
	for _, by := range order {
		value, ok := row[by.Column]
		if !ok {
			return nil, fmt.Errorf(
				"cannot cursor on column %q: the query does not return it, so there is no position to resume from", by.Column)
		}
		keys = append(keys, value)
	}
	return keys, nil
}

// sqlExecError explains a failure the wrapper could have caused. T-SQL rejects
// a bare ORDER BY inside a CTE, so a statement that ran unfiltered can stop
// running the moment a filter wraps it — and the driver's message says nothing
// about why.
func sqlExecError(dialect sqlDialect, filters []query.ColumnFilterValue, err error) error {
	if dialect == dialectSQLServer && len(filters) > 0 {
		return fmt.Errorf(
			"failed to execute filtered sql query (an ORDER BY inside the query cannot survive column filtering on sqlserver; move it out or add OFFSET 0 ROWS): %w", err)
	}
	return fmt.Errorf("failed to execute sql query: %w", err)
}

func (p sqlProvider) open(ctx context.Context, req query.ProviderRequest) (*sql.Rows, *sql.DB, *db.RowScanner, error) {
	if req.Query == "" {
		return nil, nil, nil, fmt.Errorf("sql query is required")
	}

	opts, err := query.DecodeOptions[sqlOptions](req.Options)
	if err != nil {
		return nil, nil, nil, err
	}

	client, dialect, err := sqlConnect(ctx, sqlConnectRequest{
		Connection: req.Connection, ConnType: p.connType, Options: opts,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// The dialect is only knowable here, once the connection has been hydrated,
	// so the statement is built here rather than by the caller.
	statement, args, err := buildFilteredSQL(dialect, req.Query, req.Filters)
	if err != nil {
		_ = client.Close()
		return nil, nil, nil, err
	}

	// statement is the author's own query, unchanged, wrapped in a CTE built
	// from constant syntax; every filter value travels as a bound arg and every
	// filter identifier is validated against validSQLIdentifier before quoting.
	// codeql[go/sql-injection]
	rows, err := client.QueryContext(ctx, statement, args...)
	if err != nil {
		_ = client.Close()
		return nil, nil, nil, sqlExecError(dialect, req.Filters, err)
	}
	scanner, err := db.NewRowScanner(rows)
	if err != nil {
		_ = rows.Close()
		_ = client.Close()
		return nil, nil, nil, fmt.Errorf("failed to prepare sql rows: %w", err)
	}
	return rows, client, scanner, nil
}
