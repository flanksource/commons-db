package xetrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// SystemHealthSource is where the built-in system_health session writes and
// how long it may hold an event before its file sees it.
type SystemHealthSource struct {
	CurrentFile     string        `json:"currentFile"`
	FilePattern     string        `json:"filePattern"`
	DispatchLatency time.Duration `json:"dispatchLatency"`
}

const systemHealthSourceSQL = `SELECT
	CAST(t.target_data AS xml).value('(EventFileTarget/File/@name)[1]', 'nvarchar(4000)') AS current_file,
	(SELECT es.max_dispatch_latency FROM sys.server_event_sessions AS es WHERE es.name = s.name) AS dispatch_latency_ms
FROM sys.dm_xe_sessions AS s
JOIN sys.dm_xe_session_targets AS t ON t.event_session_address = s.address
WHERE s.name = 'system_health' AND t.target_name = 'event_file'`

var rolloverSuffix = regexp.MustCompile(`(?i)_\d+_\d+\.xel$`)

// ResolveSystemHealthSource locates the running system_health session's
// event files and its dispatch latency.
func ResolveSystemHealthSource(ctx context.Context, db *sql.Conn) (SystemHealthSource, error) {
	var currentFile sql.NullString
	var dispatchLatencyMs sql.NullInt64
	err := db.QueryRowContext(ctx, systemHealthSourceSQL).Scan(&currentFile, &dispatchLatencyMs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SystemHealthSource{}, fmt.Errorf("locate system_health event files (requires VIEW SERVER STATE, or VIEW SERVER PERFORMANCE STATE on SQL Server 2022+): %w", err)
	}
	if currentFile.String == "" {
		return SystemHealthSource{}, fmt.Errorf("system_health is not running with an event_file target")
	}
	if dispatchLatencyMs.Int64 <= 0 {
		return SystemHealthSource{}, fmt.Errorf("system_health has no bounded max_dispatch_latency")
	}
	pattern, err := SystemHealthFilePattern(currentFile.String)
	if err != nil {
		return SystemHealthSource{}, err
	}
	return SystemHealthSource{
		CurrentFile:     currentFile.String,
		FilePattern:     pattern,
		DispatchLatency: time.Duration(dispatchLatencyMs.Int64) * time.Millisecond,
	}, nil
}

// SystemHealthFilePattern is the glob matching every rollover file of the
// system_health session whose current file is currentFile.
func SystemHealthFilePattern(currentFile string) (string, error) {
	if !rolloverSuffix.MatchString(currentFile) {
		return "", fmt.Errorf("system_health event file %q does not end in _<n>_<n>.xel, so its rollover files cannot be matched", currentFile)
	}
	return rolloverSuffix.ReplaceAllString(currentFile, "") + "*.xel", nil
}

const lastEventFileRowSQL = `SELECT TOP (1) file_name, file_offset
FROM sys.fn_xe_file_target_read_file(@p1, NULL, NULL, NULL)
ORDER BY file_name DESC, file_offset DESC`

// AttachSystemHealth reads the built-in system_health session from the end of
// its current file onwards, so a capture sees only what happens after it
// starts. The session is not this process's: Drop releases the reader and
// leaves the session running.
func AttachSystemHealth(ctx context.Context, pool *sql.DB, opts CreateOptions) (_ *Session, err error) {
	db, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	source, err := ResolveSystemHealthSource(ctx, db)
	if err != nil {
		return nil, err
	}
	session := &Session{
		Name: SystemHealthSession, db: db, pool: pool, opts: opts,
		filePattern: source.FilePattern, dispatchLatency: source.DispatchLatency,
	}
	var fileName string
	var offset int64
	err = db.QueryRowContext(ctx, lastEventFileRowSQL, source.FilePattern).Scan(&fileName, &offset)
	switch {
	case err == nil:
		session.fileCursor = fileCursor{file: fileName, offset: offset, valid: true}
	case !errors.Is(err, sql.ErrNoRows) && !isFileNotYetCreated(err):
		return nil, fmt.Errorf("baseline system_health event file: %w", err)
	}
	stats, err := session.sessionStats(ctx)
	if err != nil {
		return nil, err
	}
	session.droppedBaseline = stats.DroppedCount
	return session, nil
}
