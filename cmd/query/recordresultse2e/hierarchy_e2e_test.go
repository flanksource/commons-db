package recordresultse2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/cmd/query/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

var invocationPath = "/api/v1/profile/" + url.PathEscape("trace-results/invocation")

var _ = Describe("hierarchical record results", Ordered, func() {
	var server resultServer

	BeforeAll(func() {
		ctx := context.Background()
		schemas := recordstore.NewSchemas()
		source := recordresultstest.NewKV(schemas)
		registry := newSampleRegistry(source, schemas)
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.InvocationResult]{
			Kind: "invocation", Title: "Invocations", KeyColumn: "id",
			Hierarchy: &recordresults.HierarchyColumns{ID: "id", Parent: "parentId"},
		})).To(Succeed())
		_, err := recordstore.AppendTyped(ctx, source, "run-1", "invocation", []recordresultstest.InvocationResult{
			{ID: "child-a", ParentID: "root-a", Name: "child", Depth: 3},
			{ID: "root-a", ParentID: "unrecorded-a", Name: "alpha", Depth: 2},
			{ID: "child-b", ParentID: "root-b", Name: "child", Depth: 5},
			{ID: "root-b", ParentID: "unrecorded-b", Name: "beta", Depth: 4},
			{ID: "root-c", Name: "gamma", Depth: 0},
		})
		Expect(err).ToNot(HaveOccurred())
		_, err = recordstore.AppendTyped(ctx, source, "run-2", "invocation", []recordresultstest.InvocationResult{
			{ID: "unrecorded-a", Name: "another stream's caller"},
		})
		Expect(err).ToNot(HaveOccurred())
		server = resultServer{source: source, registry: registry, handler: serveResults(registry)}
	})

	rows := func(query string) ([]map[string]any, http.Header) {
		response := server.get(invocationPath+"?stream=run-1&"+query, "application/json")
		ExpectWithOffset(1, response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result []map[string]any
		ExpectWithOffset(1, json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed(), response.Body.String())
		return result, response.Header()
	}

	It("declares an optional boolean root-only parameter", func() {
		profile, err := server.registry.Get(context.Background(), "trace-results/invocation")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Params).To(ContainElement(query.ParamDef{
			Name: "rootsOnly", Label: "Roots only", Type: query.ParamTypeBoolean, Default: false,
			Description: "Show only rows whose parent is not recorded in this stream",
		}))
	})

	It("shows the stored forest's provisional roots while callers can still arrive", func() {
		all, header := rows("limit=10")
		Expect(header.Get("X-Total-Count")).To(Equal("5"))
		Expect(all).To(HaveLen(5))

		roots, header := rows("rootsOnly=true&limit=10")
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(recordresultstest.Column(roots, "id")).To(Equal([]any{"root-a", "root-b", "root-c"}))
	})

	It("filters and pages over roots rather than filtering a page of all rows", func() {
		first, header := rows("rootsOnly=true&limit=1")
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(recordresultstest.Column(first, "id")).To(Equal([]any{"root-a"}))
		Expect(header.Get("X-Has-More")).To(Equal("true"))

		second, header := rows("rootsOnly=true&limit=1&cursor=" + url.QueryEscape(header.Get("X-Next-Cursor")))
		Expect(recordresultstest.Column(second, "id")).To(Equal([]any{"root-b"}))
		Expect(header.Get("X-Total-Count")).To(Equal("3"))

		filtered, header := rows("rootsOnly=true&filter.name=alpha&limit=10")
		Expect(recordresultstest.Column(filtered, "id")).To(Equal([]any{"root-a"}))
		Expect(header.Get("X-Total-Count")).To(Equal("1"))
	})

	It("reconciles a provisional root when its caller arrives in the same stream", func() {
		_, err := recordstore.AppendTyped(context.Background(), server.source, "run-1", "invocation", []recordresultstest.InvocationResult{
			{ID: "unrecorded-a", Name: "caller", Depth: 1},
		})
		Expect(err).ToNot(HaveOccurred())
		roots, header := rows("rootsOnly=true&limit=10")
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(recordresultstest.Column(roots, "id")).To(ConsistOf("unrecorded-a", "root-b", "root-c"))
	})

	It("keeps unresolved roots visible after sealing, at any raw depth", func() {
		Expect(server.source.Seal(context.Background(), "run-1")).To(Succeed())
		roots, header := rows("rootsOnly=true&limit=10")
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(recordresultstest.Column(roots, "id")).To(ConsistOf("unrecorded-a", "root-b", "root-c"))
	})
})
