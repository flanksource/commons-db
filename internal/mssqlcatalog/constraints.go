// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
import "context"

type Index = native.Index

func Indexes(ctx context.Context, q Queryer, scope Scope) ([]Index, error) {
	return native.Indexes(ctx, q, scope)
}

type IndexColumn = native.IndexColumn

func IndexColumns(ctx context.Context, q Queryer, scope Scope) ([]IndexColumn, error) {
	return native.IndexColumns(ctx, q, scope)
}

type ForeignKey = native.ForeignKey

func ForeignKeys(ctx context.Context, q Queryer, scope Scope) ([]ForeignKey, error) {
	return native.ForeignKeys(ctx, q, scope)
}

type CheckConstraint = native.CheckConstraint

func CheckConstraints(ctx context.Context, q Queryer, scope Scope) ([]CheckConstraint, error) {
	return native.CheckConstraints(ctx, q, scope)
}
