package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/golang-sql/sqlexp"
)

// runSQLServerBatch runs one T-SQL batch through go-mssqldb's message loop.
//
// The loop is what surfaces a batch's whole story in order: each result set,
// each DONE count (MsgRowsAffected, summed into RowsAffected), PRINT and
// low-severity RAISERROR text (MsgNotice) and the errors the server raised
// (MsgError). No NOCOUNT handling is needed: go-mssqldb skips the row-count
// tokens between result sets, so an EXEC whose procedure deletes and then
// selects still returns its rows, and with NOCOUNT ON the counts are simply
// absent. A server error does not end the loop — T-SQL often continues past
// one — so the loop drains the batch and then fails with every error seen.
// Each notice also goes to notice as it arrives, before the batch ends.
func runSQLServerBatch(ctx context.Context, conn *sql.Conn, text string, args []any, notice func(string)) (query.BatchResult, error) {
	messages := &sqlexp.ReturnMessage{}
	rows, err := conn.QueryContext(ctx, text, append(append([]any{}, args...), messages)...)
	if err != nil {
		return query.BatchResult{}, err
	}
	defer func() { _ = rows.Close() }()
	var result query.BatchResult
	var affected int64
	var counted bool
	var failures []error
	for active := true; active; {
		switch message := messages.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			printed := message.Message.String()
			result.Messages = append(result.Messages, printed)
			notice(printed)
		case sqlexp.MsgNext:
			set, hasColumns, err := readResultSet(rows)
			if err != nil {
				failures = append(failures, err)
			} else if hasColumns {
				result.ResultSets = append(result.ResultSets, set)
			}
		case sqlexp.MsgRowsAffected:
			affected, counted = affected+message.Count, true
		case sqlexp.MsgError:
			failures = append(failures, message.Error)
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		}
	}
	if err := rows.Err(); err != nil {
		failures = append(failures, err)
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		return query.BatchResult{}, fmt.Errorf("sql server: %w", errors.Join(failures...))
	}
	if counted {
		result.RowsAffected = new(affected)
	}
	return result, nil
}
