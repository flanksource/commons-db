// Package viewdeps preserves the commons-db API; implementations live in sqlglot-go.
package viewdeps

import native "github.com/flanksource/sqlglot-go/database/migrate/viewdeps"
import "context"

type DropOptions = native.DropOptions

func Sweep(ctx context.Context, opts DropOptions) ([]View, func(context.Context) error, error) {
	return native.Sweep(ctx, opts)
}
