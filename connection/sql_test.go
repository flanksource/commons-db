package connection

import (
	"net/url"
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/microsoft/go-mssqldb/msdsn"
)

func TestSQLConnectionStringAppliesResolvedCredentials(t *testing.T) {
	tests := []struct {
		name     string
		connType string
		dsn      string
		assert   func(*testing.T, string)
	}{
		{
			name:     "postgres URL",
			connType: models.ConnectionTypePostgres,
			dsn:      "postgres://localhost:5432/app?sslmode=disable",
			assert: func(t *testing.T, got string) {
				parsed, err := url.Parse(got)
				if err != nil {
					t.Fatal(err)
				}
				assertURLUser(t, parsed)
			},
		},
		{
			name:     "mysql DSN",
			connType: models.ConnectionTypeMySQL,
			dsn:      "tcp(localhost:3306)/app",
			assert: func(t *testing.T, got string) {
				cfg, err := mysql.ParseDSN(got)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.User != "app-user" || cfg.Passwd != "p@ss/word" {
					t.Fatalf("credentials not applied: user=%q password-set=%t", cfg.User, cfg.Passwd != "")
				}
			},
		},
		{
			name:     "sqlserver ADO DSN",
			connType: models.ConnectionTypeSQLServer,
			dsn:      "server=localhost;database=app;encrypt=disable",
			assert: func(t *testing.T, got string) {
				cfg, err := msdsn.Parse(got)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.User != "app-user" || cfg.Password != "p@ss/word" {
					t.Fatalf("credentials not applied: user=%q password-set=%t", cfg.User, cfg.Password != "")
				}
			},
		},
		{
			name:     "clickhouse URL",
			connType: models.ConnectionTypeClickHouse,
			dsn:      "clickhouse://localhost:9000/app",
			assert: func(t *testing.T, got string) {
				parsed, err := url.Parse(got)
				if err != nil {
					t.Fatal(err)
				}
				assertURLUser(t, parsed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := SQLConnection{
				Type:     tt.connType,
				URL:      types.EnvVar{ValueStatic: tt.dsn},
				Username: types.EnvVar{ValueStatic: "app-user"},
				Password: types.EnvVar{ValueStatic: "p@ss/word"},
			}
			got, err := conn.connectionString()
			if err != nil {
				t.Fatal(err)
			}
			tt.assert(t, got)
		})
	}
}

func assertURLUser(t *testing.T, parsed *url.URL) {
	t.Helper()
	if parsed.User == nil || parsed.User.Username() != "app-user" {
		t.Fatalf("username not applied: %v", parsed.User)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ss/word" {
		t.Fatalf("password not applied: password-set=%t", ok)
	}
}
