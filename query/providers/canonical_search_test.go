package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Portable canonical execution", func() {
	It("keeps one profile's logical conditions unchanged across different source layouts", func() {
		profile := query.Profile{Name: "portable", Render: query.RenderTraces, Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{"index": "spans"}}, Search: &esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpBool, Conditions: []esdsl.Condition{{Op: esdsl.OpTerm, Field: "service", Value: esdsl.Literal("checkout")}, {Op: esdsl.OpRange, Field: "duration_ms", Gte: &esdsl.Value{Param: "minimumDuration"}}}}}, Params: []query.ParamDef{{Name: "minimumDuration", Default: 0.2}}, Columns: query.DefaultSpanColumns()}
		original, err := json.Marshal(profile.Search)
		profile.Columns = append(profile.Columns, query.ColumnDef{Name: "attempts", CEL: "row._span_attributes.attempt * 2"})
		Expect(err).NotTo(HaveOccurred())
		for _, jaeger := range []bool{true, false} {
			fields := map[string]string{"trace_id": "keyword", "span_id": "keyword", "timestamp": "date", "duration_ms": "double", "service": "keyword", "operation": "keyword"}
			document := map[string]any{"trace_id": "t", "span_id": "s", "timestamp": "2026-11-02T10:00:00.0001Z", "duration_ms": 0.25, "service": "checkout", "operation": "request"}
			expectedService, expectedDuration := "service", "duration_ms"
			if jaeger {
				fields = map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process.serviceName": "keyword", "operationName": "keyword"}
				document = map[string]any{"traceID": "t", "spanID": "s", "startTimeMillis": int64(1793613600000), "duration": 250, "process.serviceName": "checkout", "operationName": "request"}
				expectedService, expectedDuration = "process.serviceName", "duration"
			}
			var searches []map[string]any
			fields["attributes.attempt"] = "long"
			document["attributes"] = map[string]any{"attempt": 3}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer GinkgoRecover()
				if r.Method == http.MethodHead {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/_mapping/field/") {
					Expect(json.NewEncoder(w).Encode(map[string]any{"spans": map[string]any{"mappings": map[string]any{"timestamp": map[string]any{"full_name": "timestamp", "mapping": map[string]any{"timestamp": map[string]any{"type": "date"}}}}}})).To(Succeed())
					return
				}
				if strings.HasSuffix(r.URL.Path, "_field_caps") {
					caps := map[string]any{}
					for name, typ := range fields {
						caps[name] = map[string]any{typ: map[string]any{"searchable": true, "aggregatable": true}}
					}
					Expect(json.NewEncoder(w).Encode(map[string]any{"fields": caps})).To(Succeed())
					return
				}
				Expect(r.URL.Path).To(Equal("/spans/_search"))
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				searches = append(searches, body)
				Expect(json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"total": map[string]any{"value": 1, "relation": "eq"}, "hits": []any{map[string]any{"_id": "s", "_source": document}}}})).To(Succeed())
			}))
			for _, provider := range []string{"opensearch", "opentelemetry"} {
				profile.Provider.Type = provider
				ctx := context.New()
				if provider == "opentelemetry" {
					profile.Provider.Connection = "connection://traces"
					delete(profile.Provider.Options, "address")
					ctx = ctx.WithDB(traceConnections(server.URL), nil)
				} else {
					profile.Provider.Connection = ""
					profile.Provider.Options["address"] = server.URL
				}
				result, err := query.Execute(ctx, profile, map[string]any{"filter.service": "checkout,!staging"})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Rows).To(HaveLen(1))
				Expect(result.Rows[0]["service"]).To(Equal("checkout"))
				Expect(result.Rows[0]["duration_ms"]).To(Equal(0.25))
				Expect(result.Rows[0]["attempts"]).To(BeNumerically("==", 6))
				Expect(searches).NotTo(BeEmpty())
				encoded, err := json.Marshal(searches[0])
				Expect(err).NotTo(HaveOccurred())
				Expect(string(encoded)).To(ContainSubstring(fmt.Sprintf(`"%s":"checkout"`, expectedService)))
				Expect(string(encoded)).To(ContainSubstring(`"` + expectedDuration + `"`))
				after, err := json.Marshal(profile.Search)
				Expect(err).NotTo(HaveOccurred())
				Expect(after).To(Equal(original))
			}
			server.Close()
		}
	})
})
