// Specs for the trace plugin OpenAPI: the kinds list, and one start operation
// per kind whose request body is the kind's own params form.

package sessions

import (
	"time"

	"github.com/flanksource/clicky/rpc"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/tracing/traces"
)

var _ = ginkgo.Describe("Trace plugin OpenAPI", func() {
	ginkgo.It("describes the kinds list and a start operation per kind taking its params form", func() {
		kinds := traces.NewKinds()
		Expect(kinds.RegisterKind("pings", traces.NewHandler[pingParams, ping](pings{}, traces.Capabilities{Live: true}))).To(Succeed())
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "traces", ConnectionName: "traces",
			Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite), Register: kinds.RegisterResultTypes,
		})
		runtime := &traces.Runtime{Kinds: kinds, Results: results, Probes: probe.NewManager(probe.ManagerOptions{}), PollEvery: time.Second}

		spec := &rpc.OpenAPISpec{}
		AddTracesOpenAPI(func() *traces.Runtime { return runtime })(spec)

		Expect(spec.Paths).To(HaveKey("/api/v1/traces/kinds"))
		start := spec.Paths["/api/v1/traces/pings/sessions"]["post"]
		Expect(start.OperationID).To(Equal("start-pings-trace"))
		Expect(start.Responses).To(HaveKey("201"))
		body := start.RequestBody.Content["application/json"].Schema
		Expect(body.Properties["params"].Properties).To(HaveKey("count"))
		Expect(body.Properties["params"].Properties["count"].Default).To(BeEquivalentTo(1))
		Expect(body.Properties["duration"].Type).To(Equal("string"))
	})

	ginkgo.It("describes nothing before the server serves trace kinds", func() {
		spec := &rpc.OpenAPISpec{}
		AddTracesOpenAPI(func() *traces.Runtime { return nil })(spec)
		Expect(spec.Paths).To(BeEmpty())
	})
})
