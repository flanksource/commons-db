package sqlactivity

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	sqlServerProgressAge = time.Minute
	sqlServerDetailsAge  = 5 * time.Minute
)

// sqlServerActivity reads, in one batch, the server time and every user
// session's requests but the poller's own. A session without a request is
// read when it holds an open transaction, the blocker a sleeping session so
// often is, or when @idle asks for idle sessions; its SQL is the last batch
// its connection sent. @session, when not zero, narrows the read to that one
// session. A request's SQL is the statement it is running. The DMVs' local
// datetimes take the server's offset, so they compare with the sample time.
const sqlServerActivity = pollMarker + `
DECLARE @sampled_at datetimeoffset = SYSDATETIMEOFFSET();
SELECT @sampled_at AS sampled_at;
SELECT s.session_id, COALESCE(r.request_id, 0) AS request_id, COALESCE(r.blocking_session_id, 0) AS blocked_by,
	COALESCE(DB_NAME(COALESCE(r.database_id, s.database_id)), '') AS database_name,
	COALESCE(s.login_name, '') AS login_name, COALESCE(s.program_name, '') AS program_name, COALESCE(s.host_name, '') AS host_name,
	COALESCE(r.status, s.status) AS state, COALESCE(r.command, '') AS command,
	COALESCE(r.wait_type, '') AS wait_type, COALESCE(r.wait_resource, '') AS wait_resource, r.wait_time,
	TODATETIMEOFFSET(COALESCE(r.start_time, s.last_request_start_time), DATEPART(TZOFFSET, @sampled_at)) AS started_at,
	TODATETIMEOFFSET(tx.begin_time, DATEPART(TZOFFSET, @sampled_at)) AS transaction_started_at,
	r.total_elapsed_time, r.cpu_time, r.logical_reads,
	r.reads, r.writes,
	CAST(r.granted_query_memory AS bigint) * 8192 AS memory_bytes,
	s.open_transaction_count, r.query_hash,
	COALESCE(CASE WHEN r.session_id IS NULL OR r.statement_start_offset IS NULL THEN st.text
		ELSE SUBSTRING(st.text, r.statement_start_offset / 2 + 1,
			(CASE r.statement_end_offset WHEN -1 THEN DATALENGTH(st.text) ELSE r.statement_end_offset END - r.statement_start_offset) / 2 + 1)
	END, '') AS sql_text,
	progress.percent_complete, progress.estimated_completion_time, CONVERT(varchar(20), progress.transaction_id),
	current_tx.transaction_state, progress.rows_returned, progress.last_wait_type,
	tx_log.log_records, tx_log.log_bytes_used, tx_log.log_bytes_reserved,
	CASE WHEN @progress = 1 AND r.total_elapsed_time > @details_ms THEN COALESCE(waits.waiting_tasks, 0) END,
	waits.wait_type, waits.wait_duration_ms
FROM sys.dm_exec_sessions AS s
LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = s.session_id
OUTER APPLY (SELECT TOP 1 c.most_recent_sql_handle FROM sys.dm_exec_connections AS c WHERE c.session_id = s.session_id ORDER BY c.connect_time) AS c
OUTER APPLY sys.dm_exec_sql_text(COALESCE(r.sql_handle, c.most_recent_sql_handle)) AS st
OUTER APPLY (SELECT MIN(at.transaction_begin_time) AS begin_time FROM sys.dm_tran_session_transactions AS stx
	JOIN sys.dm_tran_active_transactions AS at ON at.transaction_id = stx.transaction_id WHERE stx.session_id = s.session_id) AS tx
OUTER APPLY (
	SELECT r.percent_complete, r.estimated_completion_time, NULLIF(r.transaction_id, 0) AS transaction_id,
		r.row_count AS rows_returned, NULLIF(r.last_wait_type, '') AS last_wait_type
	WHERE @progress = 1 AND r.total_elapsed_time > @progress_ms
) AS progress
LEFT JOIN sys.dm_tran_active_transactions AS current_tx ON current_tx.transaction_id = progress.transaction_id
OUTER APPLY (
	SELECT SUM(dt.database_transaction_log_record_count) AS log_records,
		SUM(dt.database_transaction_log_bytes_used) AS log_bytes_used,
		SUM(dt.database_transaction_log_bytes_reserved) AS log_bytes_reserved
	FROM sys.dm_tran_database_transactions AS dt
	WHERE dt.transaction_id = progress.transaction_id AND r.total_elapsed_time > @details_ms
) AS tx_log
OUTER APPLY (
	SELECT TOP (1) COUNT_BIG(*) OVER () AS waiting_tasks, w.wait_type, w.wait_duration_ms
	FROM sys.dm_os_tasks AS t
	JOIN sys.dm_os_waiting_tasks AS w ON w.waiting_task_address = t.task_address
	WHERE @progress = 1 AND r.total_elapsed_time > @details_ms
		AND t.session_id = r.session_id AND t.request_id = r.request_id
	ORDER BY w.wait_duration_ms DESC, w.wait_type
) AS waits
WHERE s.is_user_process = 1 AND s.session_id <> @@SPID
	AND (r.session_id IS NOT NULL OR s.open_transaction_count > 0 OR @idle = 1)
	AND (@session = 0 OR s.session_id = @session);`

func readSQLServer(ctx context.Context, db *sql.DB, filter Filter) (result Snapshot, err error) {
	rows, err := db.QueryContext(ctx, sqlServerActivity, sql.Named("idle", filter.Idle), sql.Named("session", filter.SessionID),
		sql.Named("progress", filter.SQLServerProgress), sql.Named("progress_ms", sqlServerProgressAge.Milliseconds()),
		sql.Named("details_ms", sqlServerDetailsAge.Milliseconds()))
	if err != nil {
		return result, fmt.Errorf("read sys.dm_exec_requests: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if !rows.Next() {
		return result, errors.Join(fmt.Errorf("sql activity snapshot has no server timestamp"), rows.Err())
	}
	if err := rows.Scan(&result.ServerTime); err != nil {
		return result, fmt.Errorf("read server time: %w", err)
	}
	// The timestamp set is read to its end: the SQL Server driver moves to the
	// next result set only from the end of the current one.
	if rows.Next() {
		return result, fmt.Errorf("sql activity snapshot has multiple server timestamps")
	}
	if !rows.NextResultSet() {
		return result, errors.Join(fmt.Errorf("sql activity snapshot has no requests result set"), rows.Err())
	}
	for rows.Next() {
		row, err := scanSQLServer(rows)
		if err != nil {
			return result, err
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("read sys.dm_exec_requests: %w", err)
	}
	return result, nil
}

func scanSQLServer(rows *sql.Rows) (Sample, error) {
	var s Sample
	var startedAt, transactionStartedAt sql.NullTime
	var elapsedMs sql.NullInt64
	var waitMs, cpuMs sql.NullInt64
	var queryHash []byte
	var transactionID, lastWaitType, longestTaskWaitType sql.NullString
	var transactionState sql.NullInt64
	progress := &s.SQLServerProgress
	if err := rows.Scan(&s.SessionID, &s.RequestID, &s.BlockedBy, &s.Database, &s.User, &s.Application, &s.ClientHost,
		&s.State, &s.Command, &s.WaitType, &s.WaitResource, &waitMs, &startedAt, &transactionStartedAt,
		&elapsedMs, &cpuMs, &s.LogicalReads, &s.Reads, &s.Writes, &s.MemoryBytes, &s.OpenTransactions, &queryHash, &s.SQL,
		&progress.PercentComplete, &progress.EstimatedRemainingMs, &transactionID, &transactionState, &progress.RowsReturned, &lastWaitType,
		&progress.TransactionLogRecords, &progress.TransactionLogBytesUsed, &progress.TransactionLogBytesReserved,
		&progress.WaitingTaskCount, &longestTaskWaitType, &progress.LongestTaskWaitMs); err != nil {
		return s, fmt.Errorf("read sys.dm_exec_requests row: %w", err)
	}
	progress.TransactionID, progress.LastWaitType, progress.LongestTaskWaitType = transactionID.String, lastWaitType.String, longestTaskWaitType.String
	if transactionState.Valid {
		progress.TransactionState = sqlServerTransactionState(transactionState.Int64)
	}
	progress.normalize(s.Command, s.State)
	if waitMs.Valid {
		s.WaitMs = new(float64(waitMs.Int64))
	}
	if cpuMs.Valid {
		s.CPUMs = new(float64(cpuMs.Int64))
	}
	s.ElapsedMs = float64(elapsedMs.Int64)
	s.StartedAt, s.TransactionStartedAt = nullTime(startedAt), nullTime(transactionStartedAt)
	s.QueryID = encodeHandle(queryHash)
	return s, nil
}

// encodeHandle spells a binary handle or hash the way SQL Server prints it:
// 0x and upper-case hex. An absent one stays empty.
func encodeHandle(handle []byte) string {
	if len(handle) == 0 {
		return ""
	}
	return "0x" + strings.ToUpper(hex.EncodeToString(handle))
}
