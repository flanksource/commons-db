// Package sqlite holds the dialect-generic SQL query provider and registers it
// under the "sqlite" key. A host that only needs SQLite-backed record stores
// enables it without the cloud providers:
//
//	import _ "github.com/flanksource/commons-db/query/providers/sqlite"
//
// query/providers registers the remaining SQL keys (sql, postgres, mysql,
// sqlserver, clickhouse) through NewSQLProvider.
package sqlite

import (
	"database/sql"
	"fmt"
	"iter"
	"math"
	"strconv"

	_ "github.com/flanksource/commons-db/query/metrics"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

func init() {
	query.RegisterProvider(NewSQLProvider("sqlite", models.ConnectionTypeSQLite))
}

// NewSQLProvider returns the SQL provider registered under key. connType
// presets the connection driver type; empty takes it from the request's
// options or connection.
func NewSQLProvider(key, connType string) query.Provider {
	return sqlProvider{key: key, connType: connType}
}

// sqlProvider runs arbitrary SQL against a postgres, mysql, sqlserver, or
// clickhouse connection and returns the rows as generic records.
type sqlProvider struct {
	// key is the registry type this instance is registered under.
	key string
	// connType forces the connection driver type; empty means take it from options.
	connType string
}

var _ query.BackendCapabilityProvider = sqlProvider{}

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

// SupportsRequestSort reports yes: the order reaches the statement as ORDER BY
// and the keyset resume clause is built from the same columns, so a sort the
// request names is applied by the backend rather than approximated here.
func (p sqlProvider) SupportsRequestSort() bool { return true }

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
		if err := requireSQLArrayFilters(ctx, client, dialect, filtersUseArrays(req.Filters)); err != nil {
			yield(query.Page{}, err)
			return
		}

		request := req
		current := page
		remaining := page.Ceiling
		for {
			if remaining > 0 && remaining < current.Limit {
				current.Limit = remaining
			}
			read, err := ReadSQLPage(ctx, client, string(dialect), sqlPageRequest(request, current))
			if err != nil {
				yield(query.Page{}, err)
				return
			}
			batch, total, more := read.Rows, read.Total, read.HasMore
			capped := remaining > 0 && len(batch) >= remaining && more
			result := query.Page{Rows: batch, HasMore: more && !capped, Total: total, Truncated: capped}
			var keys []any
			if result.HasMore && len(req.Order) > 0 {
				keys, err = providerkit.OrderKeys(batch[len(batch)-1], req.Order)
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

func (p sqlProvider) BackendCapabilities(
	ctx context.Context,
	req query.ProviderRequest,
) (connection.BackendCapabilities, error) {
	connectRequest, err := p.connectRequest(req)
	if err != nil {
		return connection.BackendCapabilities{}, err
	}
	client, dialect, release, err := sqlConnect(ctx, connectRequest)
	if err != nil {
		return connection.BackendCapabilities{}, err
	}
	defer release()
	defer func() { _ = client.Close() }()
	return connection.ProbeBackendCapabilities(ctx, client, string(dialect))
}

func requireSQLArrayFilters(
	ctx context.Context,
	client *sql.DB,
	dialect sqlDialect,
	required bool,
) error {
	if !required {
		return nil
	}
	capabilities, err := connection.ProbeBackendCapabilities(ctx, client, string(dialect))
	if err != nil {
		return fmt.Errorf("probe SQL array filter capability: %w", err)
	}
	if err := capabilities.Require(connection.BackendCapabilityArrayFilters); err != nil {
		return fmt.Errorf("SQL array filtering is unavailable: %w", err)
	}
	return nil
}

func filtersUseArrays(filters []query.ColumnFilterValue) bool {
	for _, filter := range filters {
		if filter.Array && !filter.IsZero() {
			return true
		}
	}
	return false
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
	connectRequest, err := p.connectRequest(req)
	if err != nil {
		return nil, "", nil, err
	}
	return sqlConnect(ctx, connectRequest)
}

func (p sqlProvider) connectRequest(req query.ProviderRequest) (sqlConnectRequest, error) {
	opts, err := query.DecodeOptions[sqlOptions](req.Options)
	if err != nil {
		return sqlConnectRequest{}, err
	}
	return sqlConnectRequest{
		Connection: req.Connection, ConnType: p.connType, Options: opts,
	}, nil
}

func sqlPageRequest(req query.ProviderRequest, page query.PageRequest) SQLPageRequest {
	return SQLPageRequest{
		Connection: req.Connection, Query: req.Query, QueryArgs: req.QueryArgs, QueryIdentifiers: req.QueryIdentifiers,
		Filters: req.Filters, Order: req.Order, Position: req.Position,
		Page: page, Diagnostics: req.Diagnostics,
	}
}
