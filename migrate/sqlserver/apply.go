// Package sqlserver preserves the commons-db API; implementations live in sqlglot-go.
package sqlserver

import native "github.com/flanksource/sqlglot-go/database/migrate/sqlserver"
import "context"
import "database/sql"

type Request = native.Request

type Outcome = native.Outcome

const Applied = native.Applied

const AlreadyApplied = native.AlreadyApplied

const Failed = native.Failed

const CommitUnknown = native.CommitUnknown

type Result = native.Result

func Apply(ctx context.Context, db *sql.DB, request Request) (Result, error) {
	return native.Apply(ctx, db, request)
}
