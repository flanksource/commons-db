// Package providers contains the built-in data providers for the query engine.
// Each provider self-registers via init(); consumers enable them with a blank
// import:
//
//	import _ "github.com/flanksource/commons-db/query/providers"
package providers

import (
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/connection"
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

func (p sqlProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	iterator, err := p.OpenRows(ctx, req)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	var result []query.Row
	for iterator.Next() {
		result = append(result, iterator.Row())
	}
	if err := iterator.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (p sqlProvider) OpenRows(ctx context.Context, req query.ProviderRequest) (query.RowIterator, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("sql query is required")
	}

	opts, err := query.DecodeOptions[sqlOptions](req.Options)
	if err != nil {
		return nil, err
	}

	connType := p.connType
	if connType == "" {
		connType = opts.Type
	}

	conn := connection.SQLConnection{
		ConnectionName: req.Connection,
		Type:           connType,
	}
	if opts.URL != "" {
		resolveType := connType
		if resolveType == "" {
			resolveType = models.ConnectionTypePostgres
		}
		resolved, err := resolveInlineURL(ctx, opts.URL, resolveType)
		if err != nil {
			return nil, err
		}
		conn.URL.ValueStatic = resolved
	}

	if err := conn.HydrateConnection(ctx); err != nil {
		return nil, fmt.Errorf("failed to hydrate sql connection: %w", err)
	}
	if opts.Database != "" {
		conn, err = conn.UseDatabase(opts.Database)
		if err != nil {
			return nil, fmt.Errorf("failed to select sql database: %w", err)
		}
	}

	client, err := conn.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create sql client: %w", err)
	}

	rows, err := client.QueryContext(ctx, req.Query)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	scanner, err := db.NewRowScanner(rows)
	if err != nil {
		rows.Close()
		client.Close()
		return nil, fmt.Errorf("failed to prepare sql rows: %w", err)
	}
	return &sqlRowIterator{rows: rows, client: client, scanner: scanner}, nil
}

type sqlRowIterator struct {
	rows    *sql.Rows
	client  *sql.DB
	scanner *db.RowScanner
	closed  bool
}

func (i *sqlRowIterator) Next() bool     { return i.scanner.Next() }
func (i *sqlRowIterator) Row() query.Row { return query.Row(i.scanner.Row()) }
func (i *sqlRowIterator) Err() error     { return i.scanner.Err() }
func (i *sqlRowIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	rowErr := i.rows.Close()
	clientErr := i.client.Close()
	if rowErr != nil {
		return rowErr
	}
	return clientErr
}
