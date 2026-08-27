package query

import (
	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile sample resolution", func() {
	It("reports the paging modes, effective order, and resolved limits that produced the sample", func() {
		original := providerRegistry["postgres"]
		providerRegistry["postgres"] = sampleTestProvider{rows: []Row{{"id": 1}}}
		DeferCleanup(func() { providerRegistry["postgres"] = original })

		result, err := Sample(context.New(), Profile{
			Name:     "resolved",
			Provider: ProviderConfig{Type: "postgres"},
			Query:    "SELECT 1 AS id",
			Order:    Order{{Column: "id", Unique: true}},
			Limits:   &RowLimits{PageSize: 25, MaxPageSize: 250, MaxExportRows: 2_500},
		}, SampleOptions{Page: PageRequest{Limit: 1}})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Resolution).To(Equal(SampleResolution{
			PagingModes:  "offset",
			NativePaging: false,
			Pageable:     true,
			Order:        Order{{Column: "id", Unique: true}},
			Limits:       RowLimits{PageSize: 25, MaxPageSize: 250, MaxExportRows: 2_500},
		}))
	})
})
