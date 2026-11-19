package connections

import (
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenSearch trace HTTP contract", func() {
	It("documents search and detail with their typed response schemas", func() {
		spec := &rpc.OpenAPISpec{}
		AddConnectionsOpenAPI(spec)
		Expect(spec.Paths["/api/v1/connection/{id}/browser/traces"]["get"].Responses["200"].Content["application/json"].Schema.Properties).To(HaveKey("traces"))
		Expect(spec.Paths["/api/v1/connection/{id}/browser/traces/{traceId}"]["get"].Responses["200"].Content["application/json"].Schema.Properties).To(HaveKey("spans"))
	})
})
