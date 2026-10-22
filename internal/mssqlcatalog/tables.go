// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
import "context"

func Schemas(ctx context.Context, q Queryer, names []string) ([]string, error) {
	return native.Schemas(ctx, q, names)
}

type TableType = native.TableType

const BaseTables = native.BaseTables

const Views = native.Views

type Table = native.Table

func Tables(ctx context.Context, q Queryer, scope Scope, tableType TableType) ([]Table, error) {
	return native.Tables(ctx, q, scope, tableType)
}

type Column = native.Column

func Columns(ctx context.Context, q Queryer, scope Scope) ([]Column, error) {
	return native.Columns(ctx, q, scope)
}
