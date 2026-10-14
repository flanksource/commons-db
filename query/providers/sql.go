// Package providers contains the built-in data providers for the query engine.
// Each provider self-registers via init(); consumers enable them with a blank
// import:
//
//	import _ "github.com/flanksource/commons-db/query/providers"
package providers

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/sqlite"
)

func init() {
	// The generic "sql" provider takes the driver from options/connection; the
	// per-engine aliases preset it so `provider.type: clickhouse` works directly.
	// Importing sqlite also registers the "sqlite" key.
	query.RegisterProvider(sqlite.NewSQLProvider("sql", ""))
	query.RegisterProvider(sqlite.NewSQLProvider("postgres", models.ConnectionTypePostgres))
	query.RegisterProvider(sqlite.NewSQLProvider("mysql", models.ConnectionTypeMySQL))
	query.RegisterProvider(sqlite.NewSQLProvider("sqlserver", models.ConnectionTypeSQLServer))
	query.RegisterProvider(sqlite.NewSQLProvider("clickhouse", models.ConnectionTypeClickHouse))
}
