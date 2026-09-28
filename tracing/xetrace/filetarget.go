package xetrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"
)

// AutoPath is the FileTarget.Path value that asks Create to resolve a writable
// directory on the SQL Server host itself. It is the normal way to select the
// event_file target: a client-side guess is wrong on RDS, where the instance
// may only write to rdsEventFileDir, and wrong on any instance whose data
// volume layout we do not know.
const AutoPath = "auto"

// rdsEventFileDir is the only directory an Amazon RDS SQL Server instance may
// write Extended Events files to.
const rdsEventFileDir = `D:\rdsdbdata\log`

// eventFileExt is the extension SQL Server requires on an event_file target;
// it also appends _<n>_<ticks> before it on every rollover, which is why a read
// has to glob (see readPattern).
const eventFileExt = ".xel"

// FileTarget configures a package0.event_file target: events are written to
// .xel files on the SQL Server host and read back incrementally with
// sys.fn_xe_file_target_read_file. Unlike the ring buffer it never evicts, so a
// high-volume capture keeps every event at the cost of disk on the server.
type FileTarget struct {
	// Path is where SQL Server writes the files, on ITS OWN filesystem. AutoPath
	// resolves the instance's log directory (see ResolveEventFilePath); any
	// other value is used exactly as given and must be an absolute path ending
	// in .xel.
	Path string
	// MaxFileSizeMB caps one file before SQL Server rolls over. Zero uses the
	// sqltrace.eventFile.maxFileSizeMb property.
	MaxFileSizeMB int
	// MaxRolloverFiles caps how many rolled files are kept. Zero uses the
	// sqltrace.eventFile.maxRolloverFiles property. Together with MaxFileSizeMB
	// this is the capture's whole disk budget on the server.
	MaxRolloverFiles int
}

// resolved reports whether Path is a concrete path rather than AutoPath.
func (f FileTarget) resolved() bool {
	return f.Path != "" && !strings.EqualFold(f.Path, AutoPath)
}

// readPattern is the glob a read passes to sys.fn_xe_file_target_read_file.
// SQL Server writes <base>_0_<ticks>.xel, not the configured name, so the
// literal path matches nothing.
func (f FileTarget) readPattern() string {
	return strings.TrimSuffix(f.Path, eventFileExt) + "*" + eventFileExt
}

// hostProbe is what the server says about its own platform and layout. Every
// field is read in one round trip by probeHost.
type hostProbe struct {
	HostPlatform string
	ErrorLog     string
	// RDSAdminDBID is DB_ID('rdsadmin'): non-nil only on Amazon RDS.
	RDSAdminDBID *int
}

const hostProbeSQL = `
SELECT
  CAST(SERVERPROPERTY('HostPlatform') AS nvarchar(128))     AS host_platform,
  CAST(SERVERPROPERTY('ErrorLogFileName') AS nvarchar(512)) AS error_log,
  DB_ID('rdsadmin')                                         AS rds_admin_db_id`

// probeHost reads the server's platform, error-log path and RDS marker.
func probeHost(ctx context.Context, db *sql.Conn) (hostProbe, error) {
	var platform, errorLog sql.NullString
	var rdsAdmin sql.NullInt64
	if err := db.QueryRowContext(ctx, hostProbeSQL).Scan(&platform, &errorLog, &rdsAdmin); err != nil {
		return hostProbe{}, err
	}
	probe := hostProbe{HostPlatform: platform.String, ErrorLog: errorLog.String}
	if rdsAdmin.Valid {
		id := int(rdsAdmin.Int64)
		probe.RDSAdminDBID = &id
	}
	return probe, nil
}

// isRDS reports whether the instance is an Amazon RDS one.
func (p hostProbe) isRDS() bool { return p.RDSAdminDBID != nil }

// separator is the path separator of the SERVER's filesystem, which is not
// necessarily the client's — the client here is routinely macOS against a Linux
// container or a Windows RDS instance.
func (p hostProbe) separator() string {
	if strings.EqualFold(p.HostPlatform, "Windows") || p.isRDS() {
		return `\`
	}
	return "/"
}

// eventFileDir picks the directory the instance writes .xel files to. It is
// pure so every branch is table-testable without a server.
//
// The order is deliberate: an operator override beats everything; RDS is named
// explicitly rather than inferred from its error-log path, so a future RDS
// layout change fails loudly here instead of silently writing somewhere the
// instance cannot; otherwise the error log's own directory, which is writable
// by the service account by definition.
func eventFileDir(probe hostProbe, override string) (string, error) {
	if dir := strings.TrimSpace(override); dir != "" {
		return strings.TrimRight(dir, `/\`), nil
	}
	if probe.isRDS() {
		return rdsEventFileDir, nil
	}
	dir := parentDir(probe.ErrorLog, probe.separator())
	if dir == "" {
		return "", fmt.Errorf(
			"cannot resolve a directory for the XE event_file target: SERVERPROPERTY('ErrorLogFileName') is %q on host platform %q; "+
				"pass an explicit .xel path, or set the sqltrace.eventFile.dir property",
			probe.ErrorLog, probe.HostPlatform,
		)
	}
	return dir, nil
}

// parentDir is the directory part of a path expressed in the SERVER's
// separator. path/filepath is not usable here: it answers for the client's OS.
func parentDir(path, sep string) string {
	path = strings.TrimSpace(path)
	i := strings.LastIndex(path, sep)
	if i <= 0 {
		return ""
	}
	return path[:i]
}

// ResolveEventFilePath turns an AutoPath request into a concrete file on the
// server — <log directory><sep><session name>.xel — and validates an explicit
// one. The session name keeps two concurrent captures off each other's files.
func ResolveEventFilePath(ctx context.Context, db *sql.Conn, file FileTarget, sessionName string) (string, error) {
	if file.resolved() {
		if err := validateEventFilePath(file.Path); err != nil {
			return "", err
		}
		return file.Path, nil
	}
	probe, err := probeHost(ctx, db)
	if err != nil {
		return "", fmt.Errorf("probe SQL Server host layout for the XE event_file target: %w", err)
	}
	dir, err := eventFileDir(probe, eventFileDirOverride())
	if err != nil {
		return "", err
	}
	return dir + probe.separator() + sessionName + eventFileExt, nil
}

// ValidateEventFilePath checks a caller-supplied event_file path offline, so a
// bad one fails when the args are parsed rather than at CREATE EVENT SESSION.
// AutoPath passes: it is resolved against the server in Create.
func ValidateEventFilePath(path string) error {
	if strings.EqualFold(strings.TrimSpace(path), AutoPath) {
		return nil
	}
	return validateEventFilePath(path)
}

// validateEventFilePath refuses a path SQL Server could not write, before the
// DDL is built. A relative path would resolve against the service's working
// directory, and a glob would collide with readPattern's own.
func validateEventFilePath(path string) error {
	switch {
	case !strings.HasSuffix(strings.ToLower(path), eventFileExt):
		return fmt.Errorf("event_file path %q: must end in %s", path, eventFileExt)
	case strings.ContainsAny(path, "*?"):
		return fmt.Errorf("event_file path %q: must not contain a wildcard", path)
	case !isServerAbsolutePath(path):
		return fmt.Errorf("event_file path %q: must be absolute on the SQL Server host (/var/opt/mssql/log/x.xel, or D:\\rdsdbdata\\log\\x.xel)", path)
	}
	return nil
}

// isServerAbsolutePath accepts either platform's spelling, because the path is
// interpreted by the server and the client cannot assume which one it runs.
func isServerAbsolutePath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/"), strings.HasPrefix(path, `\\`):
		return true
	case len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/'):
		return true
	}
	return false
}

// fileTargetMissingErrno is SQL Server's error when
// sys.fn_xe_file_target_read_file finds no file matching the pattern. Until the
// session's first dispatch (MAX_DISPATCH_LATENCY after it starts) no .xel file
// exists at all, so this is the expected state of an early poll rather than a
// failure.
const fileTargetMissingErrno = 25718

// isFileNotYetCreated reports the one read failure that means "the session has
// not flushed anything yet", which a poll reads as an empty snapshot.
func isFileNotYetCreated(err error) bool {
	var mssqlErr mssql.Error
	return errors.As(err, &mssqlErr) && mssqlErr.Number == fileTargetMissingErrno
}

// fileCursor is where the next read resumes: the file and offset of the last
// row the previous poll returned. sys.fn_xe_file_target_read_file skips
// everything up to and including that offset, which is what keeps a poll's cost
// proportional to the new events rather than to the whole capture.
type fileCursor struct {
	file   string
	offset int64
	valid  bool
}

const readFileSQL = `
SELECT file_name, file_offset, CAST(event_data AS nvarchar(max)) AS event_data
FROM sys.fn_xe_file_target_read_file(@p1, NULL, @p2, @p3)`

// readArgs are the three bound parameters of readFileSQL: the glob, then the
// resume file and offset, which are NULL on the first poll.
func (c fileCursor) readArgs(pattern string) []any {
	if !c.valid {
		return []any{pattern, nil, nil}
	}
	return []any{pattern, c.file, c.offset}
}

type eventFileRow struct {
	FileName   string
	FileOffset int64
	EventData  string
}

// readEventFileRows reads the event_file rows readFileSQL returns for args.
func readEventFileRows(ctx context.Context, db *sql.Conn, args []any) ([]eventFileRow, error) {
	rows, err := db.QueryContext(ctx, readFileSQL, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []eventFileRow
	for rows.Next() {
		var row eventFileRow
		if err := rows.Scan(&row.FileName, &row.FileOffset, &row.EventData); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// pollFile reads the .xel rows written since the previous poll and the
// session's own drop counters. The counters come first so a session dropped
// out from under us is reported as ErrSessionGone rather than as an endlessly
// re-readable file that has stopped growing — the file outlives the session, so
// nothing else would notice.
func (s *Session) pollFile(ctx context.Context) (TargetSnapshot, error) {
	stats, err := s.sessionStats(ctx)
	if err != nil {
		return TargetSnapshot{}, err
	}

	pattern := s.filePattern
	if pattern == "" {
		pattern = s.opts.File.readPattern()
	}
	rows, err := readEventFileRows(ctx, s.db, s.fileCursor.readArgs(pattern))
	if err != nil {
		if isFileNotYetCreated(err) {
			return TargetSnapshot{Stats: stats}, nil
		}
		return TargetSnapshot{}, fmt.Errorf("read event_file target %q: %w", pattern, err)
	}

	raws := make([]rawXMLEvent, 0, len(rows))
	for _, row := range rows {
		raw, err := parseEventDataRow(row.EventData)
		if err != nil {
			return TargetSnapshot{}, err
		}
		raws = append(raws, raw)
	}
	if n := len(rows); n > 0 {
		s.fileCursor = fileCursor{file: rows[n-1].FileName, offset: rows[n-1].FileOffset, valid: true}
	}

	events, excluded := collectEvents(raws)
	stats.EventCount = int64(len(events))
	return TargetSnapshot{Events: events, ExcludedKeys: excluded, Stats: stats}, nil
}

const sessionStatsSQL = `
SELECT dropped_event_count, dropped_buffer_count
FROM sys.dm_xe_sessions WHERE name = @p1`

// sessionStats reads the live session's own drop counters. An absent row is
// ErrSessionGone, exactly as the ring buffer's DMV join is.
func (s *Session) sessionStats(ctx context.Context) (TargetStats, error) {
	var droppedEvents, droppedBuffers sql.NullInt64
	if err := s.db.QueryRowContext(ctx, sessionStatsSQL, s.Name).Scan(&droppedEvents, &droppedBuffers); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TargetStats{}, fmt.Errorf("%w: %q", ErrSessionGone, s.Name)
		}
		return TargetStats{}, fmt.Errorf("read event session state: %w", err)
	}
	// TotalEventsProcessed is deliberately left at zero: an event_file target
	// publishes no such counter, and a file target does not evict, so Drain's
	// eviction-delta arm has nothing to measure. DroppedCount still reports
	// events the server refused to buffer at all.
	return TargetStats{DroppedCount: max(droppedEvents.Int64-s.droppedBaseline, 0)}, nil
}
