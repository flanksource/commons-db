package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDescriptorForConnection(t *testing.T) {
	tests := []struct {
		connectionType string
		kind           string
		provider       string
		catalog        bool
	}{
		{models.ConnectionTypePostgres, "query", "postgres", true},
		{models.ConnectionTypeMySQL, "query", "mysql", true},
		{models.ConnectionTypeSQLServer, "query", "sqlserver", true},
		{models.ConnectionTypeClickHouse, "query", "clickhouse", true},
		{models.ConnectionTypeHTTP, "query", "http", false},
		{models.ConnectionTypePrometheus, "query", "prometheus", false},
		{models.ConnectionTypeLoki, "query", "loki", false},
		{models.ConnectionTypeOpenSearch, "query", "opensearch", true},
		{models.ConnectionTypeJaeger, "query", "jaeger", false},
		{models.ConnectionTypeRedis, "cache", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.connectionType, func(t *testing.T) {
			descriptor, ok := descriptorForConnection(tt.connectionType)
			if !ok {
				t.Fatal("expected browser descriptor")
			}
			if descriptor.Kind != tt.kind || descriptor.Provider != tt.provider || descriptor.Catalog != tt.catalog {
				t.Fatalf("descriptor = %#v", descriptor)
			}
			if descriptor.Kind == "query" {
				props, _ := descriptor.OptionsSchema["properties"].(queryschema.Schema)
				for _, override := range []string{"url", "address", "type"} {
					if _, found := props[override]; found {
						t.Errorf("browser options expose forbidden override %q", override)
					}
				}
			}
		})
	}
	if _, ok := descriptorForConnection(models.ConnectionTypeSlack); ok {
		t.Fatal("notification connections must keep the default detail view")
	}
}

func TestConnectionBrowserDescriptorAndHTTPScoping(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`CREATE TABLE connections (
        id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
        url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
        insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
    )`).Error; err != nil {
		t.Fatal(err)
	}
	conn := models.Connection{ID: uuid.New(), Name: "api", Type: models.ConnectionTypeHTTP, URL: "https://example.test/api"}
	if err := gdb.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	ctx := dbcontext.NewContext(context.Background()).WithDB(gdb, nil)
	handler := newConnectionBrowserHandler("/api/v1", ctx, http.NotFoundHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/connection/"+conn.ID.String()+"/browser", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("descriptor status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var descriptor browserDescriptor
	if err := json.Unmarshal(recorder.Body.Bytes(), &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.Provider != "http" || descriptor.DefaultQuery != "/" {
		t.Fatalf("descriptor = %#v", descriptor)
	}

	body := bytes.NewBufferString(`{"query":"https://attacker.invalid/data","options":{"method":"GET"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/connection/"+conn.ID.String()+"/browser/query", body)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("absolute URL status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSQLReturnsRows(t *testing.T) {
	for _, statement := range []string{"SELECT 1", " with x as (select 1) select * from x", "SHOW TABLES", "EXPLAIN SELECT 1"} {
		if !sqlReturnsRows(statement) {
			t.Errorf("expected row-producing statement: %q", statement)
		}
	}
	for _, statement := range []string{"INSERT INTO t VALUES (1)", "UPDATE t SET a=1", "DELETE FROM t", "CREATE TABLE t(a int)", "EXEC p"} {
		if sqlReturnsRows(statement) {
			t.Errorf("expected non-row statement: %q", statement)
		}
	}
}

func TestSQLIdentifier(t *testing.T) {
	if got := sqlIdentifier(models.ConnectionTypePostgres, "public", "events"); got != `"public"."events"` {
		t.Fatalf("postgres identifier = %s", got)
	}
	if got := sqlIdentifier(models.ConnectionTypeMySQL, "app", "events"); got != "`app`.`events`" {
		t.Fatalf("mysql identifier = %s", got)
	}
	if got := sqlIdentifier(models.ConnectionTypeSQLServer, "dbo", "events"); got != "[dbo].[events]" {
		t.Fatalf("sqlserver identifier = %s", got)
	}
}
