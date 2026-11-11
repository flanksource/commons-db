package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/tracequery"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("OpenSearch trace browser integration", func() {
	It("searches and opens whole traces through the connection HTTP routes", func() {
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodHead {
				return
			}
			if strings.HasSuffix(r.URL.Path, "/_field_caps") {
				Expect(json.NewEncoder(w).Encode(map[string]any{"fields": map[string]any{"@timestamp": map[string]any{"date": map[string]any{"searchable": true, "aggregatable": true}}}})).To(Succeed())
				return
			}
			if strings.Contains(r.URL.Path, "/_mapping/field/") {
				Expect(json.NewEncoder(w).Encode(map[string]any{"otel-traces-example": map[string]any{"mappings": map[string]any{"@timestamp": map[string]any{"full_name": "@timestamp", "mapping": map[string]any{"@timestamp": map[string]any{"type": "date"}}}}}})).To(Succeed())
				return
			}
			var body map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			hits := []any{}
			if body["search_after"] == nil {
				for i, id := range []string{"root", "child"} {
					doc := map[string]any{"@timestamp": at.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano), "trace_id": "trace-example", "span_id": id, "service_name": "checkout", "operation_name": id, "duration_ms": 100, "status": map[string]any{"code": 2}}
					if i > 0 {
						doc["parent_id"] = "root"
					}
					hits = append(hits, map[string]any{"_index": "otel-traces-example", "_id": id, "_source": doc, "sort": []any{i, id}})
				}
			}
			Expect(json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": len(hits), "relation": "eq"}, "hits": hits}})).To(Succeed())
		}))
		DeferCleanup(upstream.Close)
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Exec(`CREATE TABLE connections (id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT, url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT, insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT)`).Error).To(Succeed())
		connection := models.Connection{ID: uuid.New(), Name: "search", Type: models.ConnectionTypeOpenSearch, URL: upstream.URL}
		Expect(db.Create(&connection).Error).To(Succeed())
		handler := newConnectionBrowserHandler("/api/v1", dbcontext.NewContext(context.Background()).WithDB(db, nil), http.NotFoundHandler())
		base := "/api/v1/connection/" + connection.ID.String() + "/browser/traces"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"?minSpans=2", nil))
		Expect(response.Code).To(Equal(200), response.Body.String())
		var result tracequery.SearchResult
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("SpanCount", 2), HaveField("Errors", 2))))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/trace-example", nil))
		Expect(response.Code).To(Equal(200), response.Body.String())
		var detail tracequery.Detail
		Expect(json.Unmarshal(response.Body.Bytes(), &detail)).To(Succeed())
		Expect(detail.Spans).To(HaveLen(2))
		Expect(detail.Spans[1].ParentID).To(Equal("root"))
	})
})
