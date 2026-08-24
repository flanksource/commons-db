package profiles

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type sampleLookupProvider struct{}

func (sampleLookupProvider) Type() string { return "opensearch" }

func (sampleLookupProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (sampleLookupProvider) LookupFilterValues(
	dbcontext.Context,
	query.ProviderRequest,
	query.ColumnFilterBinding,
	string,
	int,
) ([]query.FilterOption, *query.Total, error) {
	return []query.FilterOption{{Value: "api", Count: 3}}, &query.Total{Value: 1, Exact: true}, nil
}

var _ = Describe("profile sampling errors", func() {
	It("returns an Oops JSON error with context for an invalid request", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		response := post(handler, "/api/v1/profile/sample", `{"profile":{"profile":"sample","_id":"ui-only"}}`)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))

		var payload map[string]any
		Expect(json.Unmarshal(response.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload).To(HaveKeyWithValue("error", ContainSubstring(`unknown field "_id"`)))
		Expect(payload).To(HaveKeyWithValue("trace", Not(BeEmpty())))
		Expect(payload).To(HaveKeyWithValue("stacktrace", Not(BeEmpty())))
		Expect(payload).To(HaveKeyWithValue("context", HaveKeyWithValue("operation", "profile.sample")))
	})

	It("accepts the explicit processor preview mode", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		response := post(handler, "/api/v1/profile/sample", `{
			"profile":{"profile":"sample","provider":{"type":"custom"}},
			"previewProcessors":true,
			"refreshInspection":true,
			"filters":{},
			"filterColumns":[{"name":"message","type":"string"}]
		}`)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`sampling provider \"custom\" is disabled`))
		Expect(response.Body.String()).NotTo(ContainSubstring(`unknown field \"previewProcessors\"`))
		Expect(response.Body.String()).NotTo(ContainSubstring(`unknown field \"refreshInspection\"`))
		Expect(response.Body.String()).NotTo(ContainSubstring(`unknown field \"filters\"`))
		Expect(response.Body.String()).NotTo(ContainSubstring(`unknown field \"filterColumns\"`))
	})

	It("serves draft profile filter values through the sample route", func() {
		original, err := query.GetProvider("opensearch")
		Expect(err).ToNot(HaveOccurred())
		query.RegisterProvider(sampleLookupProvider{})
		DeferCleanup(func() { query.RegisterProvider(original) })

		handler := newProfileSampleFilterValuesHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		response := post(handler, "/api/v1/profile/sample/filters/values", `{
			"profile":{"profile":"sample","query":"{}","provider":{"type":"opensearch"}},
			"filterColumns":[{"name":"service","type":"string"}],
			"filterKey":"filter.service",
			"search":"ap",
			"limit":20
		}`)

		Expect(response.Code).To(Equal(http.StatusOK))
		var payload map[string]any
		Expect(json.Unmarshal(response.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload).To(Equal(map[string]any{
			"options":       []any{map[string]any{"value": "api", "count": float64(3)}},
			"total":         float64(1),
			"totalRelation": "eq",
		}))
	})

	It("lifts an armed filter lookup into the request recorder", func() {
		original, err := query.GetProvider("opensearch")
		Expect(err).ToNot(HaveOccurred())
		query.RegisterProvider(sampleLookupProvider{})
		DeferCleanup(func() { query.RegisterProvider(original) })

		handler := newProfileSampleFilterValuesHandler("/api/v1", dbcontext.New(), http.NotFoundHandler())
		capture := query.NewRecorder(query.RecorderOptions{ID: "sample-filter-values", Level: logger.Debug})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/profile/sample/filters/values", bytes.NewBufferString(`{
			"profile":{"profile":"sample","query":"{}","provider":{"type":"opensearch"}},
			"filterColumns":[{"name":"service","type":"string"}],
			"filterKey":"filter.service"
		}`))
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, devtools.RequestWithRecorder(request, capture))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(capture.Summary().Counts.Operations).To(Equal(1))
		Expect(capture.Summary().Counts.Inspections).To(Equal(1))
	})
})
