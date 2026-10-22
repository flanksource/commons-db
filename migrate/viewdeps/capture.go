// Package viewdeps preserves the commons-db API; implementations live in sqlglot-go.
package viewdeps

import native "github.com/flanksource/sqlglot-go/database/migrate/viewdeps"
import "context"

type Definition = native.Definition

func Capture(ctx context.Context, q Querier, views []View) ([]Definition, error) {
	return native.Capture(ctx, q, views)
}

type RestoreError = native.RestoreError

func Restore(ctx context.Context, exec Exec, defs []Definition) error {
	return native.Restore(ctx, exec, defs)
}
