package profiles

import (
	"encoding/json"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
)

var _ = Describe("Mapping sample endpoint", func() {
	It("returns normalized values and available columns from canonical mappings", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), nil, &recordingHandler{})
		response := post(handler, "/api/v1/profile/sample/mapping", `{"profile":{"render":"traces","spans":{"service":{"source":"component"}}},"rows":[{"traceID":"t","spanID":"s","startTime":1793613600000100,"duration":250,"component":"checkout"}]}`)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result query.MappingPreview
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.NormalizedRows[0]["service"]).To(Equal("checkout"))
		Expect(result.AvailableColumns).NotTo(ContainElement(HaveField("Name", "component")))
	})
	It("keeps numeric sample fields numeric for CEL mappings", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), nil, &recordingHandler{})
		response := post(handler, "/api/v1/profile/sample/mapping", `{"profile":{"columns":[{"name":"doubled","cel":"row.value * 2.0"}]},"rows":[{"value":3}]}`)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result query.MappingPreview
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Rows[0]["doubled"]).To(Equal(float64(6)))
	})
	It("maps retained Jaeger rows without resolving a provider", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), nil, &recordingHandler{})
		response := post(handler, "/api/v1/profile/sample/mapping", `{"profile":{"profile":"draft","render":"traces","columns":[{"name":"title","cel":"row.span_id + ' mapped'"}]},"rows":[{"traceID":"trace","spanID":"span","startTime":1793613600000100,"duration":250}]}`)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result query.MappingPreview
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Rows[0]["title"]).To(Equal("span mapped"))
		Expect(result.Rows[0]).To(HaveKey("_trace"))
	})
	It("reports invalid spans", func() {
		handler := newProfileSampleHandler("/api/v1", dbcontext.New(), nil, &recordingHandler{})
		response := post(handler, "/api/v1/profile/sample/mapping", `{"profile":{"render":"traces"},"rows":[{}]}`)
		Expect(response.Code).To(Equal(http.StatusUnprocessableEntity))
	})
})
