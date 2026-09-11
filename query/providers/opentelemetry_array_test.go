package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenTelemetry array filters", func() {
	DescribeTable("reports native document array capability without probing the network",
		func(provider query.BackendCapabilityProvider, request query.ProviderRequest, backend, database string) {
			capabilities, err := provider.BackendCapabilities(dbcontext.New(), request)
			Expect(err).ToNot(HaveOccurred())
			Expect(capabilities).To(SatisfyAll(
				HaveField("Backend", backend),
				HaveField("Version", ""),
				HaveField("Database", database),
				HaveField("Features", map[dbconnection.BackendCapability]dbconnection.BackendCapabilityStatus{
					dbconnection.BackendCapabilityArrayFilters: {Supported: true, Detail: "native multi-valued fields"},
				}),
			))
		},
		Entry("OpenSearch", opensearchProvider{}, query.ProviderRequest{
			Options: map[string]any{"index": "logs-*"},
		}, "opensearch", "logs-*"),
		Entry("OpenTelemetry", openTelemetryProvider{}, query.ProviderRequest{},
			"opentelemetry", "otel-traces-*"),
	)

	It("compiles array elements through the OpenSearch terms DSL", func() {
		options := openTelemetryOptions{}
		options.withDefaults()
		request, err := buildOpenTelemetryRequest(query.ProviderRequest{
			Filters: []query.ColumnFilterValue{{
				Field: "resource.attributes.labels", Array: true,
				Include: []string{"customer", "managed"}, Exclude: []string{"internal"},
			}},
		}, options, openSearchPage{}, nil)
		Expect(err).ToNot(HaveOccurred())

		compiled := request.body["query"].(map[string]any)["bool"].(map[string]any)
		Expect(compiled["filter"]).To(ContainElement(map[string]any{
			"terms": map[string]any{"resource.attributes.labels": []any{"customer", "managed"}},
		}))
		Expect(compiled["must_not"]).To(Equal([]any{
			map[string]any{"terms": map[string]any{"resource.attributes.labels": []any{"internal"}}},
		}))
	})

	It("looks up array elements with document counts, search, and a distinct total", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, err := fmt.Fprint(w, `{
				"hits":{"total":{"value":0,"relation":"eq"},"hits":[]},
				"aggregations":{
					"__clicky_values":{"buckets":[{"key":"managed","doc_count":2}]},
					"__clicky_total":{"doc_count":2,"values":{"value":1}}
				}
			}`)
			Expect(err).ToNot(HaveOccurred())
		}))
		DeferCleanup(server.Close)

		ctx := dbcontext.New()
		searcher, err := opensearch.New(ctx, opensearch.Backend{Address: server.URL}, nil)
		Expect(err).ToNot(HaveOccurred())
		options, total, err := lookupOpenSearchFilterValues(
			ctx, searcher, "logs-*", nil,
			query.ColumnFilterBinding{Field: "labels", Array: true}, "man", 25,
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(options).To(Equal([]query.FilterOption{{Value: "managed", Count: 2}}))
		Expect(total).To(Equal(&query.Total{Value: 1}))

		aggregations := requestBody["aggregations"].(map[string]any)
		terms := aggregations["__clicky_values"].(map[string]any)["terms"].(map[string]any)
		Expect(terms).To(SatisfyAll(
			HaveKeyWithValue("field", "labels"),
			HaveKeyWithValue("size", float64(25)),
			HaveKeyWithValue("include", ".*man.*"),
		))
		Expect(aggregations["__clicky_total"]).To(HaveKey("filter"))
	})
})
