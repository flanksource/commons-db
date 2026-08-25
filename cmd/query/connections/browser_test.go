package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/flanksource/commons/logger"
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
		{models.ConnectionTypeSQLite, "query", "sqlite", true},
		{models.ConnectionTypeHTTP, "query", "http", false},
		{models.ConnectionTypePrometheus, "query", "prometheus", false},
		{models.ConnectionTypeLoki, "query", "loki", false},
		{models.ConnectionTypeOpenSearch, "query", "opensearch", true},
		// Elasticsearch is read by the same searcher, so it browses as OpenSearch.
		{models.ConnectionTypeElasticSearch, "query", "opensearch", true},
		{models.ConnectionTypeOpenTelemetry, "query", "opentelemetry", true},
		{models.ConnectionTypeJaeger, "query", "jaeger", false},
		{models.ConnectionTypeAWS, "query", "cloudwatch", false},
		// One connection type, two providers: Cloud Logging is the log browser
		// and BigQuery stays profile-only.
		{models.ConnectionTypeGCP, "query", "gcpcloudlogging", false},
		{models.ConnectionTypeAzure, "query", "azureloganalytics", false},
		{models.ConnectionTypeKubernetes, "query", "k8s", false},
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
				// The browser edits the query's own limit and a profile's three
				// caps; the connection has no profile, so it declares the defaults
				// those caps fall back to rather than leaving the frontend to
				// guess at 100, 1000 and 100000.
				if descriptor.RowLimits == nil ||
					descriptor.RowLimits.PageSize != query.DefaultPageSize ||
					descriptor.RowLimits.MaxPageSize != query.DefaultMaxPageSize ||
					descriptor.RowLimits.MaxExportRows != query.DefaultMaxExportRows {
					t.Fatalf("row limits = %#v", descriptor.RowLimits)
				}
				props, _ := descriptor.OptionsSchema["properties"].(queryschema.Schema)
				for _, override := range []string{"url", "address", "type", "endpoint"} {
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

// A browser query is a bounded top-N, so the order the provider returns and the
// cut it applies are one decision. Every logs view therefore has to say which
// way its rows already run, or the browser sorts them itself and shows the cut
// as if it had been made the other way round.
func TestLogsDescriptorsDeclareTheOrderTheyReturn(t *testing.T) {
	want := map[string]string{
		models.ConnectionTypeLoki:       "desc", // direction=backward
		models.ConnectionTypeAWS:        "desc", // default query sorts @timestamp desc
		models.ConnectionTypeGCP:        "desc", // logadmin.NewestFirst
		models.ConnectionTypeAzure:      "desc", // default query tops by TimeGenerated
		models.ConnectionTypeKubernetes: "asc",  // the kubelet API only resumes forward
	}
	for connectionType, dir := range want {
		t.Run(connectionType, func(t *testing.T) {
			descriptor, ok := descriptorForConnection(connectionType)
			if !ok {
				t.Fatal("expected browser descriptor")
			}
			if descriptor.ResultView != "logs" {
				t.Fatalf("result view = %q, want logs", descriptor.ResultView)
			}
			if descriptor.ResultSort == nil {
				t.Fatal("logs descriptor must declare the order it returns rows in")
			}
			if descriptor.ResultSort.Key != "timestamp" || descriptor.ResultSort.Dir != dir {
				t.Fatalf("result sort = %#v, want timestamp %s", descriptor.ResultSort, dir)
			}
		})
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
		case "/logs/_search":
			_, _ = w.Write([]byte(`{
				"took":3,
				"timed_out":false,
				"hits":{"total":{"value":1,"relation":"eq"},"hits":[
					{"_index":"logs","_id":"event-1","_score":1,"_source":{"message":"ready"}}
				]}
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

	body := bytes.NewBufferString(`{"query":"{\"query\":{\"match_all\":{}}}","options":{"index":"logs"}}`)
	recorder = httptest.NewRecorder()
	// Armed the way the middleware arms it, at the level that buys a preview.
	// The request body no longer carries a debug flag: what a query explains is
	// the console's decision, not the caller's.
	capture := query.NewRecorder(query.RecorderOptions{ID: "browser-query", Level: logger.Trace})
	handler.ServeHTTP(recorder, devtools.RequestWithRecorder(httptest.NewRequest(http.MethodPost,
		"/api/v1/connection/"+conn.ID.String()+"/browser/query", body), capture))
	if recorder.Code != http.StatusOK {
		t.Fatalf("query status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserQueryResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Diagnostics == nil || !strings.Contains(result.Diagnostics.Request.Query, "match_all") {
		t.Fatalf("request diagnostics = %#v", result.Diagnostics)
	}
	if result.Diagnostics.Response.Preview == "" || result.Diagnostics.Response.ReturnedRows != 1 {
		t.Fatalf("response diagnostics = %#v", result.Diagnostics.Response)
	}
	// The same run is also a row in the console, which is the point of moving
	// the flag out of the body: one execution, explained in two places.
	operations := capture.Detail().Operations
	if len(operations) != 1 || operations[0].Provider != "opensearch" {
		t.Fatalf("recorded operations = %#v", operations)
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

// A read-only connection is read-only for every statement, not just the ones
// that fail to look like a SELECT. sqlReturnsRows answers where a statement is
// dispatched; it cannot answer whether the statement writes, and a write that
// opens with a row-producing keyword used to reach the database because the
// gate was asked the first question and read as an answer to the second.
func TestReadOnlyConnectionRejectsRowProducingWrites(t *testing.T) {
	readOnly := &models.Connection{Name: "snapshot", ReadOnly: true}
	writable := &models.Connection{Name: "primary"}

	writes := []string{
		"WITH removed AS (DELETE FROM jobs RETURNING *) SELECT * FROM removed",
		"EXPLAIN ANALYZE INSERT INTO jobs VALUES (1)",
		"SELECT 1; DROP TABLE jobs",
		"DELETE FROM jobs",
		"PRAGMA journal_mode = WAL",
	}
	for _, statement := range writes {
		t.Run("rejected/"+strings.Fields(statement)[0], func(t *testing.T) {
			err := readOnlyStatementError(readOnly, statement)
			if err == nil {
				t.Fatalf("expected %q to be refused on a read-only connection", statement)
			}
			if !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("error should name the read-only connection, got %v", err)
			}
			if readOnlyStatementError(writable, statement) != nil {
				t.Fatalf("%q must still run on a writable connection", statement)
			}
		})
	}

	reads := []string{
		"SELECT * FROM jobs",
		"WITH rows AS (SELECT 1) SELECT * FROM rows",
		"EXPLAIN SELECT * FROM jobs",
		"PRAGMA table_info(jobs)",
		"SELECT 'DELETE FROM jobs' AS message",
	}
	for _, statement := range reads {
		t.Run("allowed/"+strings.Fields(statement)[0], func(t *testing.T) {
			if err := readOnlyStatementError(readOnly, statement); err != nil {
				t.Fatalf("%q must still run on a read-only connection: %v", statement, err)
			}
		})
	}
}

func TestBrowserPageRequestUsesBoundedOffsetPaging(t *testing.T) {
	tests := []struct {
		name    string
		input   browserPaginationRequest
		want    query.PageRequest
		wantErr string
	}{
		{name: "defaults", want: query.PageRequest{Limit: query.DefaultPageSize, Strategy: query.PagingOffset}},
		{name: "explicit", input: browserPaginationRequest{Limit: 25, Offset: 50}, want: query.PageRequest{Limit: 25, Offset: 50, Strategy: query.PagingOffset}},
		{name: "negative offset", input: browserPaginationRequest{Limit: 25, Offset: -1}, wantErr: "page offset"},
		{name: "oversized", input: browserPaginationRequest{Limit: query.DefaultMaxPageSize + 1}, wantErr: "page limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.input.PageRequest()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PageRequest: %v", err)
			}
			if got != tt.want {
				t.Fatalf("PageRequest = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestInspectionContextHidesClickHouseDeadlineWithoutLosingTimeout(t *testing.T) {
	ctx, cancel := inspectionContext(context.Background(), models.ConnectionTypeClickHouse, 10*time.Millisecond)
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("clickhouse inspection exposes deadline %s", deadline)
	}

	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("clickhouse inspection context error = %v, want %v", ctx.Err(), context.DeadlineExceeded)
		}
	case <-time.After(time.Second):
		t.Fatal("clickhouse inspection context did not time out")
	}
}

func TestInspectionContextKeepsDeadlineForOtherConnections(t *testing.T) {
	ctx, cancel := inspectionContext(context.Background(), models.ConnectionTypePostgres, time.Minute)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("postgres inspection context must expose its deadline")
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
