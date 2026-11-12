package providers_test

import (
	"fmt"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
	"net/http/httptest"
)

var _ = Describe("OpenSearch trace normalization", func() {
	It("normalizes Jaeger source fields before aliases despite a synthesized log timestamp", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_index":"jaeger-spans","_id":"one","_source":{"traceID":"trace","spanID":"root","startTime":1793613600000100,"duration":250,"operationName":"GET /orders","process":{"serviceName":"checkout"},"tags":[{"key":"cached","value":false}]}}]}}`)
		}))
		DeferCleanup(server.Close)
		result, err := query.Execute(context.New(), query.Profile{
			Name: "Jaeger source", Render: query.RenderTraces,
			Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{"address": server.URL, "index": "jaeger-*"}},
			Query:    `{"query":{"match_all":{}}}`,
			Aliases:  []query.AliasDef{{Name: "canonical_id", CEL: "row.span_id"}},
			Columns:  []query.ColumnDef{{Name: "display", Source: "canonical_id"}},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]["display"]).To(Equal("root"))
		Expect(result.Rows[0]["timestamp"]).To(Equal("2026-11-02T10:00:00.0001Z"))
		Expect(result.Rows[0]["duration_ms"]).To(Equal(0.25))
		Expect(result.Rows[0]["_span_attributes"]).To(HaveKeyWithValue("cached", false))
	})
})
