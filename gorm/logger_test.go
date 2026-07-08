package gorm

import (
	"testing"

	commons "github.com/flanksource/commons/logger"
)

func TestClassifySQLLevel(t *testing.T) {
	tests := []struct {
		name         string
		sql          string
		rows         int64
		schemaChange bool
		base         commons.LogLevel
		want         commons.LogLevel
	}{
		{
			name:         "marked create is base level",
			sql:          "CREATE TABLE examples (id text)",
			schemaChange: true,
			want:         commons.Info,
		},
		{
			name: "unmarked create is one level below base",
			sql:  "CREATE TABLE examples (id text)",
			want: commons.Debug,
		},
		{
			name:         "marked alter is base level",
			sql:          "ALTER TABLE examples ADD COLUMN name text",
			schemaChange: true,
			want:         commons.Info,
		},
		{
			name:         "marked drop is base level",
			sql:          "DROP INDEX idx_examples_name",
			schemaChange: true,
			want:         commons.Info,
		},
		{
			name: "insert with rows is base level",
			sql:  "INSERT INTO examples (id) VALUES ('1')",
			rows: 1,
			want: commons.Info,
		},
		{
			name: "update without rows is one level below base",
			sql:  "UPDATE examples SET name = 'n' WHERE id = 'missing'",
			rows: 0,
			want: commons.Debug,
		},
		{
			name: "delete with rows is base level",
			sql:  "DELETE FROM examples WHERE id = '1'",
			rows: 1,
			want: commons.Info,
		},
		{
			name: "select is one level below base",
			sql:  "SELECT * FROM information_schema.tables",
			rows: 4,
			want: commons.Debug,
		},
		{
			name: "catalog count is one level below base",
			sql:  "SELECT count(*) FROM pg_catalog.pg_indexes",
			rows: 1,
			want: commons.Debug,
		},
		{
			name: "classification respects non-info base",
			sql:  "SELECT 1",
			base: commons.Trace,
			want: commons.Trace1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := commons.Info
			if tt.base != 0 {
				base = tt.base
			}
			if got := classifySQLLevel(tt.sql, tt.rows, base, tt.schemaChange); got != tt.want {
				t.Fatalf("classifySQLLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatementLevelOffset(t *testing.T) {
	tests := []struct {
		name                   string
		loggerName             string
		defaultStatementOffset bool
		props                  map[string]string
		want                   commons.LogLevel
	}{
		{
			name:                   "db logger offsets when db level is inherited",
			loggerName:             "db",
			defaultStatementOffset: true,
			want:                   commons.Trace,
		},
		{
			name:                   "log.level.db disables default offset",
			loggerName:             "db",
			defaultStatementOffset: true,
			props:                  map[string]string{"log.level.db": "info"},
			want:                   0,
		},
		{
			name:                   "db.log.level disables default offset",
			loggerName:             "db",
			defaultStatementOffset: true,
			props:                  map[string]string{"db.log.level": "debug"},
			want:                   0,
		},
		{
			name:                   "explicit non-db logger disables default offset",
			loggerName:             "jobs",
			defaultStatementOffset: true,
			props:                  map[string]string{"log.level.jobs": "trace"},
			want:                   0,
		},
		{
			name:       "caller-selected base level disables default offset",
			loggerName: "db",
			want:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statementLevelOffset(tt.loggerName, tt.defaultStatementOffset, func(key string) string {
				return tt.props[key]
			})
			if got != tt.want {
				t.Fatalf("statementLevelOffset() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuccessfulStatementLevelWithDefaultOffset(t *testing.T) {
	offset := statementLevelOffset("db", true, func(string) string { return "" })

	modification := classifySQLLevel("INSERT INTO examples (id) VALUES ('1')", 1, commons.Info, false) + offset
	if modification != commons.Trace {
		t.Fatalf("modification level = %v, want %v", modification, commons.Trace)
	}

	query := classifySQLLevel("SELECT * FROM pg_catalog.pg_indexes", 1, commons.Info, false) + offset
	if query != commons.Trace1 {
		t.Fatalf("query level = %v, want %v", query, commons.Trace1)
	}

	explicitOffset := statementLevelOffset("db", true, func(key string) string {
		if key == "log.level.db" {
			return "info"
		}
		return ""
	})
	explicitModification := classifySQLLevel("INSERT INTO examples (id) VALUES ('1')", 1, commons.Info, false) + explicitOffset
	if explicitModification != commons.Info {
		t.Fatalf("explicit modification level = %v, want %v", explicitModification, commons.Info)
	}
}
