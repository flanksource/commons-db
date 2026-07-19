package profiles

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type nextMarker struct{ hit bool }

func (n *nextMarker) ServeHTTP(http.ResponseWriter, *http.Request) { n.hit = true }

func get(handler http.Handler, path, accept string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// execMock is a query.Provider that echoes a fixed row set and records the query
// it was asked to run (so param templating can be asserted).
type execMock struct {
	rows []query.Row
	last query.ProviderRequest
}

func (m *execMock) Type() string { return "exec-mock" }
func (m *execMock) Execute(_ dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	m.last = req
	return m.rows, nil
}

func newExecTest(t *testing.T, p query.Profile) (*execHandler, *nextMarker, *execMock) {
	t.Helper()
	mock := &execMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
	query.RegisterProvider(mock)

	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(context.Background(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	next := &nextMarker{}
	return newExecHandler("/api/v1", dbcontext.New(), store, next), next, mock
}

func execProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "exec-mock"},
		Query:    "select * where region = '{{.params.region}}'",
		Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}}},
	}
}

func TestExecHandlerExecutesProfileWithParams(t *testing.T) {
	h, next, mock := newExecTest(t, execProfile("activities"))

	rec := get(h, "/api/v1/profile/activities?region=EU", "")
	if next.hit {
		t.Fatal("expected exec handler to serve, not delegate")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if mock.last.Query != "select * where region = 'EU'" {
		t.Fatalf("param not templated into query: %q", mock.last.Query)
	}
}

func TestExecHandlerRejectsInvalidParam(t *testing.T) {
	h, _, _ := newExecTest(t, execProfile("activities"))
	rec := get(h, "/api/v1/profile/activities?region=MARS", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid enum value", rec.Code)
	}
}

func TestExecHandlerDelegatesSchemaRequest(t *testing.T) {
	h, next, _ := newExecTest(t, execProfile("activities"))
	_ = get(h, "/api/v1/profile/activities", SchemaContentType)
	if !next.hit {
		t.Fatal("expected schema request to be delegated to next")
	}
}

func TestExecHandlerDelegatesListAndOtherPaths(t *testing.T) {
	for _, path := range []string{"/api/v1/profile", "/api/v1/connection", "/api/v1/profile/a/b"} {
		h, next, _ := newExecTest(t, execProfile("activities"))
		_ = get(h, path, "")
		if !next.hit {
			t.Fatalf("expected delegation for %q", path)
		}
	}
}

func TestExecHandlerMissingProfile(t *testing.T) {
	h, _, _ := newExecTest(t, execProfile("activities"))
	rec := get(h, "/api/v1/profile/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestExecHandlerRequestsOpenTelemetryMappingForImportRoot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []query.Profile{
		{Name: "jaeger", Provider: query.ProviderConfig{Type: "opentelemetry"}},
		{Name: "jms", Imports: []string{"jaeger"}, Provider: query.ProviderConfig{Type: "opentelemetry"}},
	} {
		if err := store.Save(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}
	handler := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
	response := get(handler, "/api/v1/profile/jms", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "profile_connection_required" || body["mappingProfile"] != "jaeger" || body["connectionType"] != "opentelemetry" {
		t.Fatalf("unexpected mapping response: %#v", body)
	}
}

func TestExecHandlerRejectsMappingForNonOpenTelemetryProfile(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name:     "sql",
		Provider: query.ProviderConfig{Type: "postgres"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/profile/sql/connection", bytes.NewBufferString(`{"connection":"connection://traces"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `expected "opentelemetry"`) {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestExecHandlerPersistsOpenTelemetryMappingOnImportRoot(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []query.Profile{
		{Name: "jaeger", Provider: query.ProviderConfig{Type: "opentelemetry"}},
		{Name: "jms", Imports: []string{"jaeger"}, Provider: query.ProviderConfig{Type: "opentelemetry"}},
	} {
		if err := store.Save(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`CREATE TABLE connections (
id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Connection{ID: uuid.New(), Name: "traces", Type: models.ConnectionTypeOpenTelemetry}).Error; err != nil {
		t.Fatal(err)
	}
	handler := newExecHandler("/api/v1", dbcontext.New().WithDB(database, nil), store, &nextMarker{})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/profile/jms/connection", bytes.NewBufferString(`{"connection":"connection://traces"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	jaeger, err := store.Get(context.Background(), "jaeger")
	if err != nil {
		t.Fatal(err)
	}
	if jaeger.Provider.Connection != "connection://traces" {
		t.Fatalf("mapping persisted to %q", jaeger.Provider.Connection)
	}
	jms, err := store.Get(context.Background(), "jms")
	if err != nil {
		t.Fatal(err)
	}
	if jms.Provider.Connection != "" {
		t.Fatalf("child profile was modified: %+v", jms.Provider)
	}
}

type execStreamMock struct {
	rows []query.Row
	last query.ProviderRequest
}

func (m *execStreamMock) Type() string { return "exec-stream" }
func (m *execStreamMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}
func (m *execStreamMock) OpenRows(_ dbcontext.Context, req query.ProviderRequest) (query.RowIterator, error) {
	m.last = req
	return query.SliceRows(m.rows), nil
}

func newExecStreamTest(t *testing.T, rows []query.Row, columns []query.ColumnDef) *execHandler {
	t.Helper()
	query.RegisterProvider(&execStreamMock{rows: rows})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{Name: "export", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows", Columns: columns}); err != nil {
		t.Fatal(err)
	}
	return newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
}

func TestExecHandlerExportsCurrentPageCSV(t *testing.T) {
	h := newExecStreamTest(t,
		[]query.Row{{"id": 1, "name": "one"}, {"id": 2, "name": "two"}, {"id": 3, "name": "three"}},
		[]query.ColumnDef{{Name: "id", Label: "ID"}, {Name: "name"}},
	)
	rec := get(h, "/api/v1/profile/export?format=csv&limit=1&offset=1", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("status=%d content-type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec.Header().Get("X-Page-Limit") != "1" || rec.Header().Get("X-Page-Offset") != "1" {
		t.Fatalf("missing page headers: %v", rec.Header())
	}
	if got := rec.Body.String(); got != "ID,Name\n2,two\n" {
		t.Fatalf("unexpected csv: %q", got)
	}
}

func TestExecHandlerExportsStructuredColumns(t *testing.T) {
	columns := []query.ColumnDef{
		{Name: "labels", Type: query.ColumnTypeKeyValue},
		{Name: "metadata", Type: query.ColumnTypeJSON},
	}
	rows := []query.Row{{
		"labels":   map[string]any{"team": "core", "env": "prod"},
		"metadata": map[string]any{"enabled": true, "retries": 3},
	}}
	h := newExecStreamTest(t, rows, columns)

	jsonResponse := get(h, "/api/v1/profile/export?format=json", "")
	if jsonResponse.Code != http.StatusOK {
		t.Fatalf("json status=%d body=%s", jsonResponse.Code, jsonResponse.Body.String())
	}
	var decoded []map[string]any
	if err := json.Unmarshal(jsonResponse.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded[0]["labels"].(map[string]any); !ok {
		t.Fatalf("labels were flattened in JSON: %#v", decoded[0]["labels"])
	}

	csvResponse := get(h, "/api/v1/profile/export?format=csv", "")
	records, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[1][0] != "env=prod, team=core" || records[1][1] != `{"enabled":true,"retries":3}` {
		t.Fatalf("unexpected CSV: %#v", records)
	}

	clickyResponse := get(h, "/api/v1/profile/export?format=clicky-json", "")
	if got := clickyResponse.Body.String(); !strings.Contains(got, `"type": "key_value"`) || !strings.Contains(got, `"language": "json"`) {
		t.Fatalf("unexpected Clicky JSON: %s", got)
	}
}

func TestExecHandlerBoundsInteractiveStreamingRequest(t *testing.T) {
	mock := &execStreamMock{rows: make([]query.Row, 100)}
	for i := range mock.rows {
		mock.rows[i] = query.Row{"id": i}
	}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{Name: "bounded", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows"}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
	rec := get(h, "/api/v1/profile/bounded?limit=25&offset=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mock.last.MaxRows != 76 {
		t.Fatalf("provider max rows = %d, want offset + limit + lookahead = 76", mock.last.MaxRows)
	}
}

func TestParseExportRequestUsesMappedPagerNames(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile/mapped?page_size=25&skip=50", nil)
	profile := query.Profile{Params: []query.ParamDef{
		{Name: "page_size", Role: query.ParamRoleLimit},
		{Name: "skip", Role: query.ParamRoleOffset},
	}}
	got, err := parseExportRequest(req, profile)
	if err != nil {
		t.Fatal(err)
	}
	if got.limit != 25 || got.offset != 50 {
		t.Fatalf("mapped page = limit %d offset %d", got.limit, got.offset)
	}
}

func TestExecHandlerStreamsAllRowsAsNDJSON(t *testing.T) {
	rows := make([]query.Row, 2500)
	for i := range rows {
		rows[i] = query.Row{"id": i}
	}
	h := newExecStreamTest(t, rows, []query.ColumnDef{{Name: "id"}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile/export?format=ndjson&scope=all&filename=rows.ndjson&_download=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Export-Mode") != "streaming" {
		t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if count := strings.Count(strings.TrimSpace(rec.Body.String()), "\n") + 1; count != 2500 {
		t.Fatalf("expected 2500 ndjson rows, got %d", count)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "rows.ndjson") {
		t.Fatalf("missing attachment filename: %q", disposition)
	}
}

func TestExecHandlerSchemaLessAllRowsRules(t *testing.T) {
	h := newExecStreamTest(t, []query.Row{{"id": 1}, {"id": 2, "late": true}}, nil)
	if rec := get(h, "/api/v1/profile/export?scope=all&format=csv", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("schema-less CSV status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := get(h, "/api/v1/profile/export?scope=all&format=json", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"late":true`) {
		t.Fatalf("schema-less JSON status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExecHandlerRejectsOversizedPDFBeforeWriting(t *testing.T) {
	rows := make([]query.Row, maxPDFRows+1)
	for i := range rows {
		rows[i] = query.Row{"id": i}
	}
	h := newExecStreamTest(t, rows, []query.ColumnDef{{Name: "id"}})
	rec := get(h, "/api/v1/profile/export?scope=all&format=pdf", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "maximum 1000") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

var _ = io.Discard
