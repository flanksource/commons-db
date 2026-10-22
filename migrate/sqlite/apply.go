// Package sqlite preserves the commons-db API; implementations live in sqlglot-go.
package sqlite

import native "github.com/flanksource/sqlglot-go/database/migrate/sqlite"
import "context"
import "ariga.io/atlas/sql/schema"

type ReconcileOptions = native.ReconcileOptions

func ReconcileTables(ctx context.Context, database schema.ExecQuerier, options ReconcileOptions, tables ...*schema.Table) error {
	return native.ReconcileTables(ctx, database, options, tables...)
}

func PendingChanges(ctx context.Context, database schema.ExecQuerier, options ReconcileOptions, tables ...*schema.Table) ([]schema.Change, error) {
	return native.PendingChanges(ctx, database, options, tables...)
}
