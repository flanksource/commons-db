// Package snapshot preserves the commons-db API; implementations live in sqlglot-go.
package snapshot

import native "github.com/flanksource/sqlglot-go/database/inspect/snapshot"
import "context"

type Options = native.Options

type Result = native.Result

func Inspect(ctx context.Context, db Querier, options Options) (Result, error) {
	return native.Inspect(ctx, db, options)
}
