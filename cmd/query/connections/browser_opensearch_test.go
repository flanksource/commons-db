package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// browserHandlerFor registers one connection of connectionType and returns a
// handler plus the browser base path for it.
func browserHandlerFor(t *testing.T, connectionType, url string) (*connectionBrowserHandler, string) {
	t.Helper()
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
	conn := models.Connection{ID: uuid.New(), Name: "logs", Type: connectionType, URL: url, InsecureTLS: true}
	if err := gdb.Create(&conn).Error; err != nil {
		t.Fatal(err)
	}
	ctx := dbcontext.NewContext(context.Background()).WithDB(gdb, nil)
	return newConnectionBrowserHandler("/api/v1", ctx, http.NotFoundHandler()),
		"/api/v1/connection/" + conn.ID.String() + "/browser"
}

func postBrowser(t *testing.T, handler *connectionBrowserHandler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)))
	return recorder
}

func TestServeCompile(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, "https://opensearch.test")

	recorder := postBrowser(t, handler, base+"/compile", `{
		"search": {
			"timeField": "@timestamp",
			"size": 25,
			"from": 10,
			"sort": [{"field": "@timestamp", "order": "desc"}],
			"query": {"op": "bool", "conditions": [
				{"op": "term", "field": "level", "value": "error"},
				{"op": "match", "occur": "must", "field": "message", "value": {"param": "text"}}
			]}
		},
		"params": {"text": "timed out", "since": "now-1h"},
		"roles": {"since": "time-from"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("compile status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserCompileResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Size != 25 || result.From != 10 {
		t.Fatalf("compile size/from = %d/%d, want 25/10", result.Size, result.From)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(result.Query), &body); err != nil {
		t.Fatalf("compiled query is not JSON: %v\n%s", err, result.Query)
	}
	// size never enters the body: the searcher sends it as a URL parameter, so a
	// body size would be silently overridden.
	if _, present := body["size"]; present {
		t.Errorf("compiled body carries size: %s", result.Query)
	}
	if body["from"] != float64(10) {
		t.Errorf("compiled body from = %v, want 10", body["from"])
	}
	boolQuery := body["query"].(map[string]any)["bool"].(map[string]any)
	// The time-from role folds into a range on timeField; the literal and the
	// param-bound operand each land in the clause their occur names.
	assertJSONEqual(t, "filter", boolQuery["filter"],
		`[{"term":{"level":"error"}},{"range":{"@timestamp":{"gte":"now-1h"}}}]`)
	assertJSONEqual(t, "must", boolQuery["must"], `[{"match":{"message":"timed out"}}]`)
	assertJSONEqual(t, "sort", body["sort"], `[{"@timestamp":{"order":"desc"}}]`)
	if !strings.Contains(result.Query, "\n") {
		t.Errorf("compiled query is not pretty-printed: %q", result.Query)
	}
}

// The preview must show the DSL an execution produces, so a templated operand
// is interpolated before it compiles — and counts as a reference, exactly as it
// does at runtime.
func TestServeCompileInterpolatesParams(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, "https://opensearch.test")

	recorder := postBrowser(t, handler, base+"/compile", `{
		"search": {"query": {"op": "term", "field": "process.serviceName", "value": "{{.params.country}}-api"}},
		"params": {"country": "kenya"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("compile status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserCompileResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Query), &body); err != nil {
		t.Fatalf("compiled query is not JSON: %v\n%s", err, result.Query)
	}
	assertJSONEqual(t, "query", body["query"], `{"term":{"process.serviceName":"kenya-api"}}`)
}

// Only the specification is previewed, while a profile also templates its params
// into the provider options and the connection. A param the specification does
// not mention may well be referenced there, so the preview compiles rather than
// accusing it — execution, which sees the whole profile, makes that call.
func TestServeCompileKeepsParamsTheSpecificationDoesNotUse(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, "https://opensearch.test")

	recorder := postBrowser(t, handler, base+"/compile", `{
		"search": {"query": {"op": "term", "field": "level", "value": "error"}},
		"params": {"tenant": "kenya"}
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("compile status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserCompileResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Query), &body); err != nil {
		t.Fatalf("compiled query is not JSON: %v\n%s", err, result.Query)
	}
	assertJSONEqual(t, "query", body["query"], `{"term":{"level":"error"}}`)
}

func TestServeCompileRejections(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, "https://opensearch.test")
	tests := []struct {
		name     string
		body     string
		status   int
		contains string
	}{
		{
			name:     "unknown operator",
			body:     `{"search":{"query":{"op":"span_near","field":"level","value":"error"}}}`,
			status:   http.StatusUnprocessableEntity,
			contains: `unknown operator "span_near"`,
		},
		{
			name:     "required param has no value",
			body:     `{"search":{"query":{"op":"term","field":"level","value":{"param":"level"}}}}`,
			status:   http.StatusUnprocessableEntity,
			contains: `param "level" has no value`,
		},
		{
			name:     "time param with no time field",
			body:     `{"search":{"query":{"op":"match_all"}},"params":{"since":"now-1h"},"roles":{"since":"time-from"}}`,
			status:   http.StatusUnprocessableEntity,
			contains: "requires timeField",
		},
		{
			name:     "field name that would escape the DSL",
			body:     `{"search":{"query":{"op":"term","field":"level\"}","value":"error"}}}`,
			status:   http.StatusUnprocessableEntity,
			contains: "field name",
		},
		{
			name:     "misspelt condition key",
			body:     `{"search":{"query":{"op":"term","feild":"level","value":"error"}}}`,
			status:   http.StatusBadRequest,
			contains: "feild",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := postBrowser(t, handler, base+"/compile", tt.body)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.status, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), tt.contains) {
				t.Fatalf("body = %q, want it to contain %q", recorder.Body.String(), tt.contains)
			}
		})
	}
}

func TestServeCompileRejectsConnectionWithoutDSL(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypePostgres, "postgres://localhost/app")
	recorder := postBrowser(t, handler, base+"/compile", `{"search":{"query":{"op":"match_all"}}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no query DSL to compile") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

// The connection browser has no profile to hold a specification, so it carries
// one in options.search and lets the server compile it.
func TestExecuteOpenSearchCompilesOptionsSearch(t *testing.T) {
	var received string
	openSearch := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// A run also reads the mapping, to know which of the columns it returns
		// the index can narrow on. Only the search is what this test is about.
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = w.Write([]byte(`{"fields":{"level":{"keyword":{"searchable":true,"aggregatable":true}}}}`))
			return
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		received = body.String() + "\nsize=" + r.URL.Query().Get("size")
		_, _ = w.Write([]byte(`{"took":3,"hits":{"total":{"value":1,"relation":"eq"},"hits":[
			{"_index":"logs","_id":"1","_score":1,"_source":{"level":"error"}}
		]}}`))
	}))
	defer openSearch.Close()

	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, openSearch.URL)
	recorder := postBrowser(t, handler, base+"/query", `{"query":"","options":{"index":"logs","limit":"42",
		"search":{"query":{"op":"term","field":"level","value":"error"}}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("query status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result browserQueryResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0]["level"] != "error" {
		t.Fatalf("rows = %v", result.Rows)
	}
	// options.limit seeds the hit cap when the specification sets no size, and it
	// reaches the wire as a URL parameter rather than a body field.
	if !strings.Contains(received, `{"query":{"term":{"level":"error"}}}`) || !strings.Contains(received, "size=42") {
		t.Fatalf("upstream request = %q", received)
	}
}

func TestExecuteOpenSearchRejectsSpecAndRawQueryTogether(t *testing.T) {
	handler, base := browserHandlerFor(t, models.ConnectionTypeOpenSearch, "https://opensearch.test")
	recorder := postBrowser(t, handler, base+"/query", `{"query":"{\"query\":{\"match_all\":{}}}",
		"options":{"index":"logs","search":{"query":{"op":"match_all"}}}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "mutually exclusive") {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

// assertJSONEqual compares a decoded DSL fragment against the JSON it should
// equal, so nested expectations stay readable.
func assertJSONEqual(t *testing.T, name string, got any, want string) {
	t.Helper()
	var expected any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("%s: malformed expectation %q: %v", name, want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		encoded, _ := json.Marshal(got)
		t.Errorf("%s = %s, want %s", name, encoded, want)
	}
}
