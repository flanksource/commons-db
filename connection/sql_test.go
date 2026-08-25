package connection

import (
	stdcontext "context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	dbcontext "github.com/flanksource/commons-db/context"
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

func TestSQLConnectionStringUsesClickHouseURLTLS(t *testing.T) {
	tests := []struct {
		name       string
		connection SQLConnection
		wantDSN    string
		wantTLS    bool
	}{
		{
			name: "https enables TLS",
			connection: SQLConnection{
				Type: models.ConnectionTypeClickHouse,
				URL:  types.EnvVar{ValueStatic: "https://clickhouse.example.com:8443/analytics?dial_timeout=200ms"},
			},
			wantDSN: "https://clickhouse.example.com:8443/analytics?dial_timeout=200ms&secure=true",
			wantTLS: true,
		},
		{
			name: "https retains resolved credentials",
			connection: SQLConnection{
				Type:     models.ConnectionTypeClickHouse,
				URL:      types.EnvVar{ValueStatic: "https://clickhouse.example.com:8443/analytics"},
				Username: types.EnvVar{ValueStatic: "app-user"},
				Password: types.EnvVar{ValueStatic: "p@ss/word"},
			},
			wantDSN: "https://app-user:p%40ss%2Fword@clickhouse.example.com:8443/analytics?secure=true",
			wantTLS: true,
		},
		{
			name: "explicit TLS remains unchanged",
			connection: SQLConnection{
				Type: models.ConnectionTypeClickHouse,
				URL:  types.EnvVar{ValueStatic: "https://clickhouse.example.com:8443/analytics?secure=true"},
			},
			wantDSN: "https://clickhouse.example.com:8443/analytics?secure=true",
			wantTLS: true,
		},
		{
			name: "http remains without TLS",
			connection: SQLConnection{
				Type: models.ConnectionTypeClickHouse,
				URL:  types.EnvVar{ValueStatic: "http://clickhouse.example.com:8123/analytics"},
			},
			wantDSN: "http://clickhouse.example.com:8123/analytics",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.connection.connectionString()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantDSN {
				t.Fatalf("clickhouse DSN = %q, want %q", got, tt.wantDSN)
			}
			options, err := clickhouse.ParseDSN(got)
			if err != nil {
				t.Fatalf("clickhouse.ParseDSN(%q): %v", got, err)
			}
			if (options.TLS != nil) != tt.wantTLS {
				t.Fatalf("clickhouse TLS configured = %t, want %t", options.TLS != nil, tt.wantTLS)
			}
		})
	}
}

func TestSQLConnectionClientDisablesCertificateAuthForEmptyPassword(t *testing.T) {
	requestHeaders := make(chan http.Header, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestHeaders <- r.Header.Clone():
		default:
		}
		http.Error(w, "expected handshake rejection", http.StatusForbidden)
	}))
	defer server.Close()

	username := "readonly-user"
	connection := SQLConnection{
		Type:     models.ConnectionTypeClickHouse,
		URL:      types.EnvVar{ValueStatic: server.URL + "/default?skip_verify=true"},
		Username: types.EnvVar{ValueStatic: username},
	}
	client, err := connection.Client(dbcontext.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close clickhouse client: %v", err)
		}
	})

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	defer cancel()
	if err := client.PingContext(ctx); err == nil {
		t.Fatal("clickhouse handshake unexpectedly succeeded")
	}

	select {
	case headers := <-requestHeaders:
		if got := headers.Get("X-ClickHouse-User"); got != username {
			t.Fatalf("clickhouse user header = %q, want %q", got, username)
		}
		if got := headers.Get("X-ClickHouse-SSL-Certificate-Auth"); got != "off" {
			t.Fatalf("clickhouse certificate auth header = %q, want %q", got, "off")
		}
	case <-ctx.Done():
		t.Fatal("clickhouse handshake request was not received")
	}
}

func TestSQLConnectionUseDatabase(t *testing.T) {
	tests := []struct {
		connType string
		dsn      string
		assert   func(*testing.T, string)
	}{
		{models.ConnectionTypePostgres, "postgres://localhost:5432/postgres?sslmode=disable", func(t *testing.T, got string) {
			parsed, _ := url.Parse(got)
			if parsed.Path != "/app" {
				t.Fatalf("postgres path = %q", parsed.Path)
			}
		}},
		{models.ConnectionTypePostgres, "host=localhost dbname=postgres sslmode=disable", func(t *testing.T, got string) {
			if !strings.HasSuffix(got, " dbname='app'") {
				t.Fatalf("postgres DSN = %q", got)
			}
		}},
		{models.ConnectionTypeMySQL, "tcp(localhost:3306)/mysql", func(t *testing.T, got string) {
			cfg, _ := mysql.ParseDSN(got)
			if cfg.DBName != "app" {
				t.Fatalf("mysql database = %q", cfg.DBName)
			}
		}},
		{models.ConnectionTypeSQLServer, "server=localhost;database=master;encrypt=disable", func(t *testing.T, got string) {
			cfg, _ := msdsn.Parse(got)
			if cfg.Database != "app" {
				t.Fatalf("sqlserver database = %q", cfg.Database)
			}
		}},
		{models.ConnectionTypeClickHouse, "clickhouse://localhost:9000/default", func(t *testing.T, got string) {
			parsed, _ := url.Parse(got)
			if parsed.Path != "/app" {
				t.Fatalf("clickhouse path = %q", parsed.Path)
			}
		}},
	}
	for _, tt := range tests {
		connection := SQLConnection{Type: tt.connType, URL: types.EnvVar{ValueStatic: tt.dsn}}
		updated, err := connection.UseDatabase("app")
		if err != nil {
			t.Fatal(err)
		}
		tt.assert(t, updated.URL.ValueStatic)
	}
}
