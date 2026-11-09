package sqlite

import (
	"database/sql"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// readSQLProgram runs a program as the one Page it is, bounded by the walk's
// ceiling like any other read, and records it in the request's diagnostics
// and the SQL statement tap.
func readSQLProgram(
	ctx context.Context,
	client *sql.DB,
	dialect sqlDialect,
	req query.ProviderRequest,
	page query.PageRequest,
	batches []sqlBatch,
) (result query.Page, err error) {
	if err := refuseProgramPaging(req, page); err != nil {
		return query.Page{}, err
	}
	started := time.Now()
	bound, err := bindProgram(dialect, req, batches)
	if err != nil {
		req.Diagnostics.RecordError(err)
		return query.Page{}, err
	}
	statement, args, batchDetails := programRequest(bound)
	req.Diagnostics.RecordRequest(statement, args, map[string]any{"dialect": string(dialect), "program": map[string]any{"batches": batchDetails}})
	run := sqlStatementRun{Connection: req.Connection, Dialect: dialect, Statement: statement, Args: args, Started: started}
	defer func() { publishSQLStatement(run, len(result.Rows), err) }()
	defer func() {
		if err != nil {
			req.Diagnostics.RecordError(err)
		}
		req.Diagnostics.RecordResponse(started, len(result.Rows), map[string]any{"dialect": string(dialect)})
	}()
	result, err = runSQLProgram(ctx, client, dialect, bound)
	if err != nil {
		return query.Page{}, err
	}
	if page.Ceiling > 0 && len(result.Rows) > page.Ceiling {
		result.Rows, result.Truncated = result.Rows[:page.Ceiling], true
	}
	return result, nil
}
