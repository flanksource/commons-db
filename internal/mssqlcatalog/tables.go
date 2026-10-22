// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
import "context"

type Rows = native.Rows

type Queryer = native.Queryer

func FromDB(db SQLQueryer) Queryer { return native.FromDB(db) }

type SQLQueryer = native.SQLQueryer

type Scope = native.Scope

type TableFeature = native.TableFeature

func UnmanagedFeatures(ctx context.Context, q Queryer, scope Scope) ([]TableFeature, error) {
	return native.UnmanagedFeatures(ctx, q, scope)
}

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
