// Specs for declaring a result hierarchy: the relationships RegisterResultType
// refuses because they cannot identify unique callers.
package recordresults_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

var _ = Describe("declaring a result hierarchy", func() {
	DescribeTable("refuses a relationship that cannot identify unique callers",
		func(key string, hierarchy recordresults.HierarchyColumns, message string) {
			registry, _ := newRegistry()
			err := recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.InvocationResult]{
				Kind: "invocation", Title: "Invocations", KeyColumn: key, Hierarchy: &hierarchy,
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an id other than the keyed row id", "", recordresults.HierarchyColumns{ID: "id", Parent: "parentId"}, "must be the result key column"),
		Entry("one column for both ends", "id", recordresults.HierarchyColumns{ID: "id", Parent: "id"}, "must differ"),
		Entry("a missing parent column", "id", recordresults.HierarchyColumns{ID: "id", Parent: "caller"}, `hierarchy column "caller"`),
		Entry("a non-text parent column", "id", recordresults.HierarchyColumns{ID: "id", Parent: "depth"}, `hierarchy column "depth" is number`),
	)
})
