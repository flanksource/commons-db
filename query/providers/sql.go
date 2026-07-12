// Package providers contains the built-in data providers for the query engine.
// Each provider self-registers via init(); consumers enable them with a blank
// import:
//
//	import _ "github.com/flanksource/commons-db/query/providers"
package providers

import (
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
}

func (p sqlProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
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

	client, err := conn.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create sql client: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			ctx.Warnf("failed to close sql connection: %v", err)
		}
	}()

	rows, err := client.QueryContext(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			ctx.Warnf("failed to close sql rows: %v", err)
		}
	}()

	scanned, err := db.ScanRows[query.Row](rows)
	if err != nil {
		return nil, fmt.Errorf("failed to scan sql rows: %w", err)
	}

	return scanned, nil
}
