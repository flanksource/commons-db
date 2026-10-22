// Package mssqlcatalog preserves the commons-db API; implementations live in sqlglot-go.
package mssqlcatalog

import native "github.com/flanksource/sqlglot-go/database/inspect/catalog/sqlserver"
import "context"

type TableFeature = native.TableFeature

func UnmanagedFeatures(ctx context.Context, q Queryer, scope Scope) ([]TableFeature, error) {
	return native.UnmanagedFeatures(ctx, q, scope)
}
