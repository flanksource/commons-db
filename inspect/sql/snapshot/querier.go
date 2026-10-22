package snapshot

import (
	"context"
	"database/sql"
)

// Querier is implemented by a database, pinned connection or transaction.
type Querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
