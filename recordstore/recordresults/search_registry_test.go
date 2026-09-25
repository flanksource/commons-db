// Specs for declaring a result type's search columns: the searches
// RegisterResultType refuses because it could not run them.
package recordresults_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

var _ = Describe("declaring a result type's search columns", func() {
	DescribeTable("refuses a search it could not run",
		func(columns []string, message string) {
			registry, _ := newRegistry()
			err := recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
				Kind: "sample_event", Title: "Sample events", SearchColumns: columns,
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a column the type does not have", []string{"db", "missing"}, `search column "missing" is not one of its columns`),
		Entry("a column that holds no text", []string{"slow"}, `search column "slow" is boolean`),
		Entry("a column named twice", []string{"db", "db"}, `search column "db" is named twice`),
	)
})
