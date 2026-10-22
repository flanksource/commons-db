// Package viewdeps preserves the commons-db API; implementations live in sqlglot-go.
package viewdeps

import native "github.com/flanksource/sqlglot-go/database/migrate/viewdeps"
import "context"

type Table = native.Table

func Tables(schema string, names ...string) []Table { return native.Tables(schema, names...) }

type View = native.View

type Querier = native.Querier

type Exec = native.Exec

func Dependents(ctx context.Context, q Querier, tables []Table) ([]View, error) {
	return native.Dependents(ctx, q, tables)
}

func Lookup(ctx context.Context, q Querier, names ...string) ([]View, error) {
	return native.Lookup(ctx, q, names...)
}
