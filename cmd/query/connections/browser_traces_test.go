package connections

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch trace HTTP contract", func() {
	It("decodes typed trace filters and rejects malformed parameters", func() {
		request, err := browserTraceRequest(httptest.NewRequest(http.MethodGet, "/traces?minSpans=2&services=%7B%22checkout%22:%22include%22%7D", nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(request.MinSpans).To(Equal(2))
		Expect(string(request.Services["checkout"])).To(Equal("include"))
		for _, query := range []string{"minSpans=oops", "unknown=value", "limit=1&limit=2", "services=invalid"} {
			_, err := browserTraceRequest(httptest.NewRequest(http.MethodGet, "/traces?"+query, nil))
			Expect(err).To(HaveOccurred())
		}
	})
	It("documents search and detail with their typed response schemas", func() {
		spec := &rpc.OpenAPISpec{}
		AddConnectionsOpenAPI(spec)
		Expect(spec.Paths["/api/v1/connection/{id}/browser/traces"]["get"].Responses["200"].Content["application/json"].Schema.Properties).To(HaveKey("traces"))
		Expect(spec.Paths["/api/v1/connection/{id}/browser/traces/{traceId}"]["get"].Responses["200"].Content["application/json"].Schema.Properties).To(HaveKey("spans"))
	})
})
