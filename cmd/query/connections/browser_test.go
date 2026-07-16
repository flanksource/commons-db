package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
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

func TestConnectionBrowserOpenSearchInspection(t *testing.T) {
	openSearch := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/_resolve/index/*":
			_, _ = w.Write([]byte(`{
				"indices":[{"name":"logs-000001","aliases":["logs"],"attributes":[]}],
				"aliases":[{"name":"logs","indices":["logs-000001"]}],
				"data_streams":[]
			}`))
		case "/logs/_field_caps":
			_, _ = w.Write([]byte(`{
				"fields":{
					"service.name":{"keyword":{"searchable":true,"aggregatable":true}},
					"message":{"text":{"searchable":true,"aggregatable":false}}
				}
			}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer openSearch.Close()

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
	conn := models.Connection{
		ID: uuid.New(), Name: "logs", Type: models.ConnectionTypeOpenSearch,
		URL: openSearch.URL, InsecureTLS: true,
	}
	if err := gdb.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	ctx := dbcontext.NewContext(context.Background()).WithDB(gdb, nil)
	handler := newConnectionBrowserHandler("/api/v1", ctx, http.NotFoundHandler())
	baseURL := "/api/v1/connection/" + conn.ID.String() + "/browser/inspect"

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, baseURL, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("inspection status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var targets browserInspection
	if err := json.Unmarshal(recorder.Body.Bytes(), &targets); err != nil {
		t.Fatal(err)
	}
	if targets.Kind != "opensearch" || len(targets.Targets) != 2 {
		t.Fatalf("inspection = %#v", targets)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, baseURL+"?target=logs&targetKind=alias", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("field inspection status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var selected browserInspection
	if err := json.Unmarshal(recorder.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if selected.Selected == nil || selected.Selected.Target.Name != "logs" || len(selected.Selected.Fields) != 2 {
		t.Fatalf("selected inspection = %#v", selected)
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

func TestCatalogNodesForSQLPreservesRelationKinds(t *testing.T) {
	nodes := catalogNodesForSQL(models.ConnectionTypePostgres, sqlinspect.Catalog{
		Schemas: []sqlinspect.Schema{{
			Name: "public",
			Relations: []sqlinspect.Relation{
				{Name: "events", Type: "table", Columns: []sqlinspect.Column{{Name: "id", DataType: "uuid"}}},
				{Name: "latest_events", Type: "view", Columns: []sqlinspect.Column{{Name: "id", DataType: "uuid"}}},
			},
		}},
	})
	if len(nodes) != 1 || len(nodes[0].Children) != 2 {
		t.Fatalf("nodes = %#v", nodes)
	}
	kinds := map[string]string{}
	for _, relation := range nodes[0].Children {
		kinds[relation.Label] = relation.Kind
	}
	if kinds["events"] != "table" || kinds["latest_events"] != "view" {
		t.Fatalf("relation kinds = %#v", kinds)
	}
}
