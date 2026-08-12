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
	"math"
	"strconv"

	"github.com/flanksource/commons-db/context"
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

// PagingModes reports the two strategies compiled around an author's query.
// Offset supports direct page jumps; cursor paging binds a provider-owned
// keyset predicate against the declared total order.
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
		client, dialect, release, err := p.connect(ctx, req)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		defer release()
		defer func() { _ = client.Close() }()

		request := req
		current := page
		remaining := page.Ceiling
		for {
			if remaining > 0 && remaining < current.Limit {
				current.Limit = remaining
			}
			batch, total, more, err := p.readPage(ctx, client, dialect, request, current)
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			capped := remaining > 0 && len(batch) >= remaining && more
			result := query.Page{Rows: batch, HasMore: more && !capped, Total: total, Truncated: capped}
			var keys []any
			if result.HasMore && len(req.Order) > 0 {
				keys, err = orderKeys(batch[len(batch)-1], req.Order)
				if err != nil {
					yield(query.Page{}, err)
					return
				}
				result.NextKeys = keys
			}
			if !yield(result, nil) || !result.HasMore {
				return
			}
			if remaining > 0 {
				remaining -= len(batch)
			}
			if page.Mode() == query.PagingCursor {
				request.Position = query.CursorPosition{Keys: keys}
				current.Offset = 0
			} else {
				current.Offset += len(batch)
			}
		}
	}
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

// takeRowTotal removes the counting column from a row and returns what it held.
//
// It is removed rather than ignored: it is the server's bookkeeping, and a row
// that carried it onward would put an internal column into every consumer —
// exports, CEL expressions, the rendered table.
func takeRowTotal(row query.Row) (int64, bool, error) {
	value, ok := row[sqlTotalColumn]
	if !ok {
		return 0, false, nil
	}
	delete(row, sqlTotalColumn)
	// Drivers spell an integer several ways depending on the column type they
	// inferred, so the count is read by value rather than by asserting one.
	switch typed := value.(type) {
	case int64:
		return typed, true, nil
	case int32:
		return int64(typed), true, nil
	case int:
		return int64(typed), true, nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false, fmt.Errorf("sql result total %d exceeds the supported maximum %d", typed, int64(math.MaxInt64))
		}
		return int64(typed), true, nil
	case uint32:
		return int64(typed), true, nil
	case uint16:
		return int64(typed), true, nil
	case uint8:
		return int64(typed), true, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false, fmt.Errorf("sql result total %d exceeds the supported maximum %d", typed, int64(math.MaxInt64))
		}
		return int64(typed), true, nil
	case float64:
		return int64(typed), true, nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("sql result total %q is not an integer: %w", typed, err)
		}
		return parsed, true, nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("sql result total %q is not an integer: %w", typed, err)
		}
		return parsed, true, nil
	default:
		return 0, false, fmt.Errorf("sql result total has unsupported type %T", value)
	}
}

func (p sqlProvider) connect(ctx context.Context, req query.ProviderRequest) (*sql.DB, sqlDialect, func(), error) {
	if req.Query == "" {
		return nil, "", nil, fmt.Errorf("sql query is required")
	}
	opts, err := query.DecodeOptions[sqlOptions](req.Options)
	if err != nil {
		return nil, "", nil, err
	}
	return sqlConnect(ctx, sqlConnectRequest{
		Connection: req.Connection, ConnType: p.connType, Options: opts,
	})
}

func (p sqlProvider) readPage(
	ctx context.Context,
	client *sql.DB,
	dialect sqlDialect,
	req query.ProviderRequest,
	page query.PageRequest,
) (batch []query.Row, total *query.Total, more bool, err error) {
	result, err := ReadSQLPage(ctx, client, string(dialect), SQLPageRequest{
		Query: req.Query, Filters: req.Filters, Order: req.Order, Position: req.Position,
		Page: page, Diagnostics: req.Diagnostics,
	})
	return result.Rows, result.Total, result.HasMore, err
}
