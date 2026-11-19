package tracequery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch trace search", func() {
	DescribeTable("reads mapped spans through the shared provider", func(format, sourceType string) {
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			if r.Method == http.MethodHead {
				w.WriteHeader(200)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			mapping := map[string]string{"@timestamp": "date", "trace_id": "keyword", "span_id": "keyword", "parent_id": "keyword", "operation_name": "keyword", "service_name": "keyword", "duration_ms": "double"}
			if format == "jaeger" {
				mapping = mappingOf(nestedTags)
			}
			if serveMapping(w, r, mapping) {
				return
			}
			var body map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			hits := []any{}
			if body["search_after"] == nil {
				for i, id := range []string{"root", "child"} {
					started := at.Add(time.Duration(i) * 20 * time.Millisecond)
					doc := map[string]any{"@timestamp": started.Format(time.RFC3339Nano), "trace_id": "trace-search", "span_id": id, "operation_name": id, "service_name": "checkout", "duration_ms": 100}
					if i > 0 {
						doc["parent_id"] = "root"
					}
					if format == "jaeger" {
						doc = map[string]any{"startTimeMillis": started.UnixMilli(), "traceID": "trace-search", "spanID": id, "operationName": id, "process": map[string]any{"serviceName": "checkout"}, "duration": 100000}
						if i > 0 {
							doc["references"] = []any{map[string]any{"refType": "CHILD_OF", "spanID": "root"}}
						}
					}
					hits = append(hits, map[string]any{"_index": "traces-example", "_id": id, "_source": doc, "sort": []any{started.UnixMilli(), id}})
				}
			}
			Expect(json.NewEncoder(w).Encode(map[string]any{"took": 1, "hits": map[string]any{"total": map[string]any{"value": len(hits), "relation": "eq"}, "hits": hits}})).To(Succeed())
		}))
		DeferCleanup(index.Close)
		catalog, err := store.New(store.Layer{Name: store.LayerCWD, Dir: GinkgoT().TempDir()})
		Expect(err).NotTo(HaveOccurred())
		search, err := crd.FromModel(models.Connection{Name: "search", Type: models.ConnectionTypeOpenSearch, URL: index.URL})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Save(store.LayerCWD, search)).To(Succeed())
		request := Request{Source: "search", Index: "traces-*", Format: DocumentFormat(format)}
		if sourceType == "opentelemetry" {
			// The opentelemetry connection owns its index and format.
			outer, err := crd.FromModel(models.Connection{Name: "traces", Type: models.ConnectionTypeOpenTelemetry, Properties: types.JSONStringMap{"connection": "connection://search", "index": "traces-*", "format": format}})
			Expect(err).NotTo(HaveOccurred())
			Expect(catalog.Save(store.LayerCWD, outer)).To(Succeed())
			request = Request{Source: "traces"}
		}
		service := Service{Connections: catalog}
		result, err := service.Search(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Traces).To(HaveExactElements(And(HaveField("TraceID", "trace-search"), HaveField("SpanCount", 2))))
		detail, err := service.Trace(context.Background(), request, "trace-search")
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Spans[1].ParentID).To(Equal("root"))
	}, Entry("flat OpenSearch", "flat", "opensearch"), Entry("Jaeger OpenTelemetry connection", "jaeger", "opentelemetry"))
})
