// Package sqlserver preserves the commons-db API; implementations live in sqlglot-go.
package sqlserver

import native "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
import "context"
import "database/sql"
import "ariga.io/atlas/sql/migrate"
import "ariga.io/atlas/sql/schema"
import "ariga.io/atlas/sql/sqlclient"
import "github.com/flanksource/commons-db/internal/mssqlcatalog"

type Driver = native.Driver

const DriverName = native.DriverName

func OpenClient(ctx context.Context, db *sql.DB) (*sqlclient.Client, error) {
	return native.OpenClient(ctx, db)
}

func Open(db schema.ExecQuerier) (migrate.Driver, error) { return native.Open(db) }

func OpenTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*sqlclient.Tx, error) {
	return native.OpenTx(ctx, db, opts)
}

func CatalogType(c mssqlcatalog.Column) (schema.Type, error) { return native.CatalogType(c) }
