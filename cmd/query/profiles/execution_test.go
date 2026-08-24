package profiles

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flanksource/clicky/rpc"
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

func TestExecHandlerDelegatesNativeLookupRequest(t *testing.T) {
	h, next, _ := newExecTest(t, execProfile("activities"))
	_ = get(h, "/api/v1/profile/activities?__lookup=filters", "")
	if !next.hit {
		t.Fatal("expected native Clicky lookup request to be delegated to next")
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

// wholeResultProcessor deliberately does not implement query.PageProcessor, so
// a profile using it cannot be served page by page.
type wholeResultProcessor struct{}

func (wholeResultProcessor) Type() string { return "test.whole-result" }

func (wholeResultProcessor) Process(_ dbcontext.Context, _ query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	return in, nil
}

type execStreamMock struct {
	rows     []query.Row
	last     query.ProviderRequest
	lastPage query.PageRequest
}

func (m *execStreamMock) Type() string { return "exec-stream" }

func (m *execStreamMock) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (m *execStreamMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}

func (m *execStreamMock) Pages(_ dbcontext.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	m.last = req
	m.lastPage = page
	return func(yield func(query.Page, error) bool) {
		total := query.Total{Value: int64(len(m.rows)), Exact: true}
		for start := min(page.Offset, len(m.rows)); ; start += page.Limit {
			end := min(start+page.Limit, len(m.rows))
			more := end < len(m.rows)
			if !yield(query.Page{
				Rows:    m.rows[start:end],
				HasMore: more,
				Total:   &total,
			}, nil) || !more {
				return
			}
		}
	}
}

// leakProbeMock reports whether the walk it started was released. started is set
// when the sequence is entered — a real provider has its connection or its
// point-in-time by then — and released when it unwinds, which is what iter.Pull2
// does on stop(). The two are separate so a walk that never began cannot be
// mistaken for one that was cleaned up.
type leakProbeMock struct {
	rows          []query.Row
	failFirstPage bool
	started       atomic.Bool
	released      atomic.Bool
}

func (m *leakProbeMock) Type() string { return "leak-probe" }

func (m *leakProbeMock) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (m *leakProbeMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}

func (m *leakProbeMock) Pages(_ dbcontext.Context, _ query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		m.started.Store(true)
		defer m.released.Store(true)
		if m.failFirstPage {
			yield(query.Page{}, errors.New("backend failed resolving the first page"))
			return
		}
		for start := 0; start < len(m.rows); start += page.Limit {
			end := min(start+page.Limit, len(m.rows))
			if !yield(query.Page{Rows: m.rows[start:end], HasMore: end < len(m.rows)}, nil) {
				return
			}
		}
	}
}

// An all-row export opens its walk before it writes anything, so a failure
// while resolving the first page has to release the backend cursor it already
// took — a connection for SQL, a point-in-time for OpenSearch. peekPages stops
// the pull on that path; this pins it, because nothing else covers the error
// branch and the resource is only observable by its release.
//
func TestExecHandlerReleasesTheWalkWhenTheFirstPageFails(t *testing.T) {
	mock := &leakProbeMock{
		rows:          []query.Row{{"id": 1}, {"id": 2}, {"id": 3}},
		failFirstPage: true,
	}
	query.RegisterProvider(mock)

	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "leaky", Provider: query.ProviderConfig{Type: "leak-probe"}, Query: "rows",
		Columns: []query.ColumnDef{{Name: "id"}},
		Order:   query.Order{{Column: "id", Unique: true}},
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	rec := get(h, "/api/v1/profile/leaky?format=csv&scope=all", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want the first-page failure this test is built on", rec.Code, rec.Body.String())
	}
	if !mock.started.Load() {
		t.Fatal("the provider walk was never entered, so this test is not exercising the release it claims to")
	}
	if !mock.released.Load() {
		t.Fatal("the walk was never released: the export failed resolving its first page and returned holding the backend cursor")
	}
}

func newExecStreamTest(t *testing.T, rows []query.Row, columns []query.ColumnDef) *execHandler {
	t.Helper()
	query.RegisterProvider(&execStreamMock{rows: rows})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := query.Profile{
		Name: "export", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows", Columns: columns,
		// Paging past the first page needs a total order, so an export fixture
		// declares one the way a real profile has to.
		Order: query.Order{{Column: "id", Unique: true}},
	}
	if err := store.Save(context.Background(), profile); err != nil {
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
	profile := query.Profile{
		Name: "bounded", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
		Order: query.Order{{Column: "id", Unique: true}},
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
	rec := get(h, "/api/v1/profile/bounded?limit=25&offset=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The page the provider is asked for is the page the caller asked for. It
	// used to be offset+limit+1, because the offset was skipped here rather
	// than by whoever owns the cursor.
	if mock.lastPage.Limit != 25 || mock.lastPage.Offset != 50 {
		t.Fatalf("provider page = %+v, want limit 25 offset 50", mock.lastPage)
	}
	if rec.Header().Get("X-Has-More") != "true" {
		t.Fatalf("expected X-Has-More on a page with rows behind it: %v", rec.Header())
	}
}

// A profile with no declared order cannot be paged past its first page: two
// executions may interleave tied rows differently, so page 2 could repeat or
// skip rows from page 1. Refusing is the only answer that is not quietly wrong.
func TestExecHandlerRefusesToPageAnUnorderedProfile(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "unordered", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	if rec := get(h, "/api/v1/profile/unordered?limit=1&offset=1", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for a second page of an unordered profile", rec.Code)
	}
	// The first page is still answerable: it names no position.
	rec := get(h, "/api/v1/profile/unordered?limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want the first page to be served", rec.Code, rec.Body.String())
	}
	// ...and it must not invite the caller to a second one. The provider has
	// rows behind this page, but asking for them is the request refused above,
	// so reporting "more exists" here is an invitation to a guaranteed 400.
	if more := rec.Header().Get("X-Has-More"); more != "false" {
		t.Fatalf("X-Has-More=%q on an unpageable profile, want false", more)
	}
	if offset, ok := rec.Header()["X-Page-Offset"]; ok {
		t.Fatalf("X-Page-Offset=%v on an unpageable profile, want it omitted", offset)
	}
}

// The paging a surface advertises has to be the paging it will serve. An
// unpageable profile that declares an `offset` parameter puts a pager in the UI
// whose every click is a 400.
func TestProfileOpenAPIOmitsOffsetForAnUnpageableProfile(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}}}
	query.RegisterProvider(mock)

	roles := func(p query.Profile) map[string]string {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
		if err := addProfileToSpec(spec, p); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, parameter := range spec.Paths["/api/v1/profile/profile-"+slugify(p.Name)]["get"].Parameters {
			if parameter.Clicky != nil && parameter.Clicky.Role != "" {
				out[parameter.Name] = parameter.Clicky.Role
			}
		}
		return out
	}

	unordered := query.Profile{Name: "unordered", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows"}
	if got := roles(unordered); got["offset"] != "" || got["cursor"] != "" {
		t.Fatalf("unpageable profile advertises paging params: %v", got)
	} else if got["limit"] != "limit" {
		t.Fatalf("limit is valid without an order and must stay advertised: %v", got)
	}

	ordered := unordered
	ordered.Order = query.Order{{Column: "id", Unique: true}}
	if got := roles(ordered); got["offset"] != "offset" {
		t.Fatalf("pageable profile must advertise offset: %v", got)
	}
}

// An error a browser cannot read is an error the user cannot act on: without
// CORS the actionable message ("declare `order:` ...") is replaced by a generic
// network failure. The header costs nothing and does not depend on the response
// being known, so it belongs before the first thing that can fail.
func TestExecHandlerAllowsCrossOriginReadsOfErrors(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "cors", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	for _, tc := range []struct {
		name, target string
		want         int
	}{
		{"unknown profile", "/api/v1/profile/profile-missing", http.StatusNotFound},
		{"bad limit", "/api/v1/profile/cors?limit=0", http.StatusBadRequest},
		{"bad scope", "/api/v1/profile/cors?scope=sideways", http.StatusBadRequest},
		{"unpageable second page", "/api/v1/profile/cors?limit=1&offset=1", http.StatusBadRequest},
		{"served page", "/api/v1/profile/cors?limit=1", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(h, tc.target, "")
			if rec.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("Access-Control-Allow-Origin=%q on a %d response", got, rec.Code)
			}
		})
	}
}

// A preflight that does not allow the origin fails before the request it is
// clearing is ever made, so the export behind it is unreachable cross-origin
// no matter what that export would have allowed.
func TestExecHandlerPreflightAllowsTheOrigin(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/profile/anything", nil)
	req.Header.Set("Origin", "http://elsewhere.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin=%q on a preflight", got)
	}
}

// A client that has to string-match an error message breaks when the message is
// reworded. The 409 asking for a connection has always carried a code; every
// other failure now does too.
func TestExecHandlerReturnsStructuredErrors(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "coded", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	for _, tc := range []struct {
		name, target, code string
		status             int
	}{
		{"unknown profile", "/api/v1/profile/profile-missing", "profile_not_found", http.StatusNotFound},
		{"bad limit", "/api/v1/profile/coded?limit=0", "invalid_export_request", http.StatusBadRequest},
		{"bad scope", "/api/v1/profile/coded?scope=sideways", "invalid_export_request", http.StatusBadRequest},
		{"unpageable second page", "/api/v1/profile/coded?limit=1&offset=1", "query_failed", http.StatusBadRequest},
		{"all rows as clicky-json", "/api/v1/profile/coded?scope=all", "format_not_exportable", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if tc.name == "all rows as clicky-json" {
				target += "&format=clicky-json"
			}
			rec := get(h, target, "")
			if rec.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type=%q on an error body", got)
			}
			var body execError
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body is not JSON: %v (%s)", err, rec.Body.String())
			}
			if body.Code != tc.code {
				t.Fatalf("code=%q want %q (message %q)", body.Code, tc.code, body.Message)
			}
			if body.Message == "" {
				t.Fatal("error carries a code but nothing a person can read")
			}
		})
	}
}

// HEAD is how a caller reads the paging headers — X-Total-Count above all —
// without paying for the rows. The schema handler on this same path already
// answers HEAD, so refusing it here made one path disagree with itself.
func TestExecHandlerAnswersHeadWithHeadersAndNoBody(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}, {"id": 3}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "head", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
		Order: query.Order{{Column: "id", Unique: true}},
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	req := httptest.NewRequest(http.MethodHead, "/api/v1/profile/head?limit=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Total-Count"); got != "3" {
		t.Fatalf("X-Total-Count=%q, want the total a HEAD is asked for", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d body bytes", rec.Body.Len())
	}
}

// Accept is a ranked list, not an ordered one. Reading the first recognised
// entry hands a caller HTML when it weighted HTML lowest and asked for the
// clicky envelope.
func TestRequestedFormatHonoursQualityWeights(t *testing.T) {
	for _, tc := range []struct{ accept, want string }{
		{"text/html;q=0.1, application/json+clicky;q=0.9", "clicky-json"},
		{"application/json+clicky;q=0.2, text/csv;q=0.8", "csv"},
		{"text/html,application/json+clicky", "html"},
		{"text/csv;q=0, application/json", "json"},
		{"*/*", "json"},
		{"", "json"},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/profile/any", nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if got := requestedFormat(req); got != tc.want {
				t.Fatalf("requestedFormat(%q) = %q, want %q", tc.accept, got, tc.want)
			}
		})
	}
}

// An explicit ?format always wins: it is the caller naming the answer rather
// than describing what it could accept.
func TestRequestedFormatPrefersTheExplicitParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/profile/any?format=csv", nil)
	req.Header.Set("Accept", "application/json+clicky;q=1.0")
	if got := requestedFormat(req); got != "csv" {
		t.Fatalf("requestedFormat = %q, want the named format", got)
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

// An all-row export is the one request with no page to bound it, so the export
// ceiling is what stops it. It is deliberately far above a page: maxPageSize
// bounds one response, maxExportRows bounds the whole export. A profile that
// exports a large table raises its own ceiling and that number is the one that
// applies, headers included.
func TestExecHandlerBoundsAllRowExportByExportCeiling(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits *query.RowLimits
		want   int
	}{
		{name: "default", want: query.DefaultMaxExportRows},
		{name: "profile", limits: &query.RowLimits{MaxExportRows: 250_000}, want: 250_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock := &execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
			query.RegisterProvider(mock)
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			profile := query.Profile{
				Name: "ceiling", Provider: query.ProviderConfig{Type: mock.Type()},
				Query: "rows", Limits: tt.limits,
				Order: query.Order{{Column: "id", Unique: true}},
			}
			if err := store.Save(context.Background(), profile); err != nil {
				t.Fatal(err)
			}
			h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

			rec := get(h, "/api/v1/profile/ceiling?format=ndjson&scope=all", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Max-Rows"); got != strconv.Itoa(tt.want) {
				t.Fatalf("X-Max-Rows = %q, want %d", got, tt.want)
			}
			// An export whose rows fit under the ceiling is complete, and must
			// not claim otherwise.
			if got := rec.Header().Get("X-Truncated"); got != "" {
				t.Fatalf("X-Truncated = %q on an export that fit under its ceiling", got)
			}
		})
	}
}

// An export that stopped at the ceiling and said nothing is indistinguishable
// from one that finished, which is the defect this whole contract exists to
// remove. The answer is only knowable after the rows are written, which is why
// it is declared as a trailer and asserted here rather than in the headers.
func TestExecHandlerReportsAnExportCutByItsCeiling(t *testing.T) {
	const ceiling = 40
	rows := make([]query.Row, 100)
	for i := range rows {
		rows[i] = query.Row{"id": i}
	}
	query.RegisterProvider(&execStreamMock{rows: rows})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := query.Profile{
		Name: "cut", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows",
		Limits: &query.RowLimits{MaxExportRows: ceiling},
		Order:  query.Order{{Column: "id", Unique: true}},
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	// A real connection rather than a recorder: httptest.ResponseRecorder keeps
	// trailers and headers in one map, so it cannot tell a header a browser can
	// read from a trailer no browser exposes — which is the fact under test.
	server := httptest.NewServer(h)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/profile/cut?format=ndjson&scope=all")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	// This provider reports an exact total, so the cut is arithmetic the server
	// can do before it writes a byte. Knowing it up front is what makes it a
	// header rather than a trailer — a browser can read one and not the other.
	if got := response.Header.Get("X-Truncated"); got != "true" {
		t.Fatalf("X-Truncated = %q on an export cut at its %d row ceiling", got, ceiling)
	}
	if declared := response.Header.Get("Trailer"); declared != "" {
		t.Fatalf("Trailer = %q declared for a cut that was already knowable", declared)
	}
	if count := strings.Count(strings.TrimSpace(string(body)), "\n") + 1; count != ceiling {
		t.Fatalf("wrote %d rows, want the %d-row ceiling", count, ceiling)
	}
}

// A buffered all-row export is bounded by the query, not by an export ceiling,
// so it can never overflow one. Declaring a trailer it will never send costs
// every such response its Content-Length and tells the caller to wait for an
// answer that is not coming.
func TestExecHandlerDeclaresNoTrailerForABufferedExport(t *testing.T) {
	query.RegisterProvider(&execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}}})
	query.RegisterProcessor(wholeResultProcessor{})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "buffered", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows",
		Columns: []query.ColumnDef{{Name: "id"}},
		Order:   query.Order{{Column: "id", Unique: true}},
		// A processor that needs every row before any row is correct is what
		// puts this export on the buffered path.
		Processors: []query.ProcessorSpec{{Type: "test.whole-result"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	rec := get(h, "/api/v1/profile/buffered?format=csv&scope=all", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mode := rec.Header().Get("X-Export-Mode"); mode != "buffered" {
		t.Fatalf("X-Export-Mode = %q, want buffered", mode)
	}
	if declared := rec.Header().Get("Trailer"); declared != "" {
		t.Fatalf("Trailer = %q on a buffered export that cannot overflow", declared)
	}
}

// A PDF stops being readable long before the export ceiling, so it enforces a
// ceiling of its own. Reporting the profile's number instead would tell the
// caller 100,000 rows were permitted for a format that accepts 1,000.
//
// Overshooting that ceiling is refused rather than truncated (see
// TestExecHandlerRejectsOversizedPDFBeforeWriting), so this asserts the number
// a PDF that fits still reports.
func TestExecHandlerPDFReportsItsOwnCeiling(t *testing.T) {
	rows := make([]query.Row, 10)
	for i := range rows {
		rows[i] = query.Row{"id": i}
	}
	query.RegisterProvider(&execStreamMock{rows: rows})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "paged-pdf", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows",
		Columns: []query.ColumnDef{{Name: "id"}},
		Order:   query.Order{{Column: "id", Unique: true}},
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	rec := get(h, "/api/v1/profile/paged-pdf?format=pdf&scope=all", "")
	if rec.Code != http.StatusOK {
		t.Skipf("pdf rendering unavailable in this environment: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Max-Rows"); got != strconv.Itoa(maxPDFRows) {
		t.Fatalf("X-Max-Rows = %q, want the PDF ceiling %d", got, maxPDFRows)
	}
	// A PDF is buffered, so it never has an answer still to come.
	if declared := rec.Header().Get("Trailer"); declared != "" {
		t.Fatalf("Trailer = %q on a buffered PDF", declared)
	}
	if got := rec.Header().Get("X-Truncated"); got != "" {
		t.Fatalf("X-Truncated = %q on a PDF that fit inside its ceiling", got)
	}
}

// The page a caller may ask for is the profile's, not the server's: a profile
// that widens maxPageSize accepts a page the default would have refused, and
// says its own number when it refuses one.
func TestExecHandlerPageLimitFollowsProfileMaxPageSize(t *testing.T) {
	mock := &execStreamMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := query.Profile{
		Name: "pages", Provider: query.ProviderConfig{Type: mock.Type()}, Query: "rows",
		Limits: &query.RowLimits{PageSize: 5, MaxPageSize: 2000},
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	if rec := get(h, "/api/v1/profile/pages?format=ndjson&limit=1500", ""); rec.Code != http.StatusOK {
		t.Fatalf("page within the profile's maximum: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := get(h, "/api/v1/profile/pages?format=ndjson&limit=2500", "")
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "between 1 and 2000") {
		t.Fatalf("page past the profile's maximum: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get(h, "/api/v1/profile/pages?format=ndjson", ""); rec.Header().Get("X-Page-Limit") != "5" {
		t.Fatalf("default page = %q, want the profile's 5", rec.Header().Get("X-Page-Limit"))
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

// execMock implements no PagingProvider, so every page of it comes from running
// the whole query and slicing the result. X-Export-Mode describes how the rows
// were produced, not what was asked for, so it has to say so — and an all-row
// export has to be served at all, which a cursor walk cannot do here.
func TestExecHandlerReportsHowTheRowsWereActuallyProduced(t *testing.T) {
	h, _, _ := newExecTest(t, query.Profile{
		Name: "buffered", Provider: query.ProviderConfig{Type: "exec-mock"}, Query: "rows",
		Columns: []query.ColumnDef{{Name: "id"}},
		Order:   query.Order{{Column: "id", Unique: true}},
	})
	for _, scope := range []string{"page", "all"} {
		rec := get(h, "/api/v1/profile/buffered?format=csv&scope="+scope, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("scope=%s status=%d body=%s", scope, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Export-Mode"); got != "buffered" {
			t.Fatalf("scope=%s reported X-Export-Mode=%q, but the provider cannot page: the whole query ran and the result was sliced", scope, got)
		}
	}
}

// A profile with no declared columns has its schema derived from the first row
// alone, so any key that appears later is dropped without a word. scope=all has
// always refused that; a page of the same profile in the same format has the
// same hole, and document-shaped backends return heterogeneous rows routinely.
// A refusal the caller can act on beats a file that is quietly missing columns.
func TestExecHandlerRefusesTabularExportsThatWouldDropColumns(t *testing.T) {
	mock := &execMock{rows: []query.Row{{"id": 1, "a": "x"}, {"id": 2, "b": "y"}}}
	query.RegisterProvider(mock)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), query.Profile{
		Name: "noncols", Provider: query.ProviderConfig{Type: "exec-mock"}, Query: "rows",
	}); err != nil {
		t.Fatal(err)
	}
	h := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

	for _, scope := range []string{"page", "all"} {
		rec := get(h, "/api/v1/profile/noncols?format=csv&scope="+scope, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("scope=%s status=%d body=%q, want a refusal rather than a lossy file", scope, rec.Code, rec.Body.String())
		}
	}

	// Only the tabular formats flatten to a fixed column set. A structured one
	// carries whatever each row has, so it keeps serving.
	if rec := get(h, "/api/v1/profile/noncols?format=json&scope=page", ""); rec.Code != http.StatusOK {
		t.Fatalf("json export status=%d body=%q, want it unaffected", rec.Code, rec.Body.String())
	}
}
