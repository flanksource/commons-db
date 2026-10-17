package dbtarget

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type Dialect string

const (
	Postgres  Dialect = "postgres"
	SQLite    Dialect = "sqlite"
	SQLServer Dialect = "sqlserver"
)

type Target struct {
	Dialect Dialect
	DSN     string
}

func Parse(raw string) (Target, error) {
	dsn := strings.TrimSpace(raw)
	if dsn == "" {
		return Target{}, fmt.Errorf("database DSN is required")
	}
	if dsn == ":memory:" {
		return Target{}, fmt.Errorf("ambiguous SQLite memory DSN %q; use a file-backed sqlite:// DSN", dsn)
	}
	if strings.HasPrefix(strings.ToLower(dsn), "sqlite://") {
		return parseSQLite(dsn[len("sqlite://"):])
	}
	if isSQLServerKeywordDSN(dsn) {
		return Target{Dialect: SQLServer, DSN: dsn}, nil
	}
	if scheme := explicitScheme(dsn); scheme != "" {
		switch scheme {
		case "postgres", "postgresql":
			return Target{Dialect: Postgres, DSN: dsn}, nil
		case "sqlserver":
			return Target{Dialect: SQLServer, DSN: dsn}, nil
		case "mssql":
			return Target{}, fmt.Errorf("unsupported database scheme %q; use sqlserver:// for SQL Server", scheme)
		default:
			return Target{}, fmt.Errorf("unsupported database scheme %q", scheme)
		}
	}
	if !isPostgresKeywordDSN(dsn) && strings.EqualFold(filepath.Ext(pathWithoutQuery(dsn)), ".db") {
		return parseSQLite(dsn)
	}
	return Target{Dialect: Postgres, DSN: dsn}, nil
}

func parseSQLite(raw string) (Target, error) {
	path, rawQuery, _ := strings.Cut(raw, "?")
	if strings.TrimSpace(path) == "" {
		return Target{}, fmt.Errorf("SQLite DSN path is required")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Target{}, fmt.Errorf("parse SQLite DSN query: %w", err)
	}
	fileURI := strings.HasPrefix(strings.ToLower(path), "file:")
	filename := path
	if fileURI {
		if filename, err = url.PathUnescape(path[len("file:"):]); err != nil {
			return Target{}, fmt.Errorf("decode SQLite file URI %q: %w", path, err)
		}
	}
	if filename == ":memory:" || strings.EqualFold(query.Get("mode"), "memory") {
		return Target{}, fmt.Errorf("SQLite migrations require a file-backed database")
	}

	var parsed *url.URL
	if fileURI {
		parsed, err = url.Parse(path)
		if err != nil {
			return Target{}, fmt.Errorf("parse SQLite file URI: %w", err)
		}
	} else {
		absolute, resolveErr := filepath.Abs(path)
		if resolveErr != nil {
			return Target{}, fmt.Errorf("resolve SQLite path %q: %w", path, resolveErr)
		}
		parsed = &url.URL{Scheme: "file", Path: filepath.Clean(absolute)}
	}
	parsed.RawQuery = managedSQLiteQuery(query).Encode()
	return Target{Dialect: SQLite, DSN: parsed.String()}, nil
}

func managedSQLiteQuery(query url.Values) url.Values {
	managed := map[string]bool{"foreign_keys": true, "busy_timeout": true, "journal_mode": true}
	pragmas := query["_pragma"][:0]
	for _, pragma := range query["_pragma"] {
		name, _, _ := strings.Cut(pragma, "(")
		if !managed[strings.ToLower(strings.TrimSpace(name))] {
			pragmas = append(pragmas, pragma)
		}
	}
	query["_pragma"] = append(pragmas, "foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)")
	return query
}

func explicitScheme(dsn string) string {
	index := strings.Index(dsn, "://")
	if index <= 0 {
		return ""
	}
	return strings.ToLower(dsn[:index])
}

func pathWithoutQuery(dsn string) string {
	path, _, _ := strings.Cut(dsn, "?")
	return path
}

// isSQLServerKeywordDSN reports whether dsn is a go-mssqldb ODBC DSN or an ADO
// DSN: ';'-separated key=value pairs naming the server by one of the address
// keys msdsn.Parse reads. A space-separated PostgreSQL keyword DSN never
// matches.
func isSQLServerKeywordDSN(dsn string) bool {
	if strings.HasPrefix(strings.ToLower(dsn), "odbc:") {
		return true
	}
	if isPostgresKeywordDSN(dsn) {
		return false
	}
	for _, pair := range strings.Split(dsn, ";") {
		key, _, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "server", "data source", "address", "addr", "network address":
			return true
		}
	}
	return false
}

func isPostgresKeywordDSN(dsn string) bool {
	for _, field := range strings.Fields(dsn) {
		key, _, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch strings.ToLower(key) {
		case "host", "hostaddr", "port", "dbname", "user", "password", "sslmode", "service":
			return true
		}
	}
	return false
}
