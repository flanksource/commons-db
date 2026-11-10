package sqlactivity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const pollMarker = "/* commons-db:sqlactivity */"

// postgresActivity reads every client backend but the poller's own. An idle
// session is read only when $1 asks for idle sessions; one idle in a
// transaction is never idle. $2, when not zero, narrows the read to that one
// backend. query_id is read through to_jsonb so a server older than 14, which
// has no such column, reads it as NULL.
const postgresActivity = pollMarker + `
SELECT a.pid, COALESCE((pg_blocking_pids(a.pid))[1], 0) AS blocked_by,
	COALESCE(a.datname, '') AS datname, COALESCE(a.usename, '') AS usename,
	COALESCE(a.application_name, '') AS application_name, COALESCE(host(a.client_addr), '') AS client_host,
	COALESCE(a.state, '') AS state, COALESCE(a.wait_event_type, '') AS wait_event_type, COALESCE(a.wait_event, '') AS wait_event,
	a.query_start, a.xact_start, COALESCE(to_jsonb(a)->>'query_id', '') AS query_id, COALESCE(a.query, '') AS query
FROM pg_stat_activity AS a
WHERE a.backend_type = 'client backend' AND a.pid <> pg_backend_pid()
	AND (a.state IS DISTINCT FROM 'idle' OR $1)
	AND ($2 = 0 OR a.pid = $2)`

// readPostgres reads pg_stat_activity in one read-only transaction, whose
// now() stamps the snapshot: the activity view is itself a snapshot taken
// once per transaction.
func readPostgres(ctx context.Context, db *sql.DB, filter Filter) (result Snapshot, err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, tx.Rollback()) }()
	if err := tx.QueryRowContext(ctx, pollMarker+" SELECT now()").Scan(&result.ServerTime); err != nil {
		return result, fmt.Errorf("read server time: %w", err)
	}
	rows, err := tx.QueryContext(ctx, postgresActivity, filter.Idle, filter.SessionID)
	if err != nil {
		return result, fmt.Errorf("read pg_stat_activity: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		row, err := scanPostgres(rows)
		if err != nil {
			return result, err
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("read pg_stat_activity: %w", err)
	}
	return result, nil
}

func scanPostgres(rows *sql.Rows) (Sample, error) {
	var s Sample
	var waitEventType, waitEvent string
	var queryStart, xactStart sql.NullTime
	if err := rows.Scan(&s.SessionID, &s.BlockedBy, &s.Database, &s.User, &s.Application, &s.ClientHost,
		&s.State, &waitEventType, &waitEvent, &queryStart, &xactStart, &s.QueryID, &s.SQL); err != nil {
		return s, fmt.Errorf("read pg_stat_activity row: %w", err)
	}
	s.WaitType = strings.Trim(waitEventType+":"+waitEvent, ":")
	s.StartedAt, s.TransactionStartedAt = nullTime(queryStart), nullTime(xactStart)
	if s.TransactionStartedAt != (time.Time{}) {
		s.OpenTransactions = 1
	}
	s.Command = commandOf(s.SQL)
	return s, nil
}

func nullTime(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
