package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("row limits", func() {
	It("keeps the page size, the page ceiling and the export ceiling apart", func() {
		Expect(query.DefaultPageSize).To(Equal(100))
		Expect(query.DefaultMaxPageSize).To(Equal(1000))
		Expect(query.DefaultMaxExportRows).To(Equal(100000))
		Expect(query.DefaultMaxExportRows).To(BeNumerically(">", query.DefaultMaxPageSize))
	})

	// Sampling infers columns rather than reading data, so its cap is small and
	// answers a different question than the page a caller actually receives.
	It("keeps the sample cap within one page", func() {
		Expect(query.DefaultSampleLimit).To(Equal(100))
		Expect(query.DefaultSampleLimit).To(BeNumerically("<=", query.DefaultMaxPageSize))
	})
})

var _ = Describe("RowLimits", func() {
	defaults := query.RowLimits{
		PageSize:      query.DefaultPageSize,
		MaxPageSize:   query.DefaultMaxPageSize,
		MaxExportRows: query.DefaultMaxExportRows,
	}

	Describe("Resolve", func() {
		It("falls back to the defaults when the profile sets no limits", func() {
			var unset *query.RowLimits
			Expect(unset.Resolve()).To(Equal(defaults))
			Expect((&query.RowLimits{}).Resolve()).To(Equal(defaults))
		})

		It("keeps what the profile set and defaults the rest", func() {
			Expect((&query.RowLimits{PageSize: 25}).Resolve()).To(Equal(query.RowLimits{
				PageSize:      25,
				MaxPageSize:   query.DefaultMaxPageSize,
				MaxExportRows: query.DefaultMaxExportRows,
			}))
		})

		// The default is a default, not a ceiling: a profile exporting a large
		// table says so for itself and its number is the one that applies.
		It("lets a profile raise the export ceiling past the default", func() {
			Expect((&query.RowLimits{MaxExportRows: 250_000}).Resolve().MaxExportRows).To(Equal(250_000))
		})
	})

	Describe("Validate", func() {
		It("accepts limits that do not contradict each other", func() {
			var unset *query.RowLimits
			Expect(unset.Validate()).To(Succeed())
			Expect((&query.RowLimits{PageSize: 200, MaxPageSize: 2000, MaxExportRows: 250_000}).Validate()).To(Succeed())
		})

		It("rejects a limit that would return nothing", func() {
			Expect((&query.RowLimits{PageSize: -1}).Validate()).To(MatchError(ContainSubstring("pageSize")))
			Expect((&query.RowLimits{MaxPageSize: -5}).Validate()).To(MatchError(ContainSubstring("maxPageSize")))
			Expect((&query.RowLimits{MaxExportRows: -1}).Validate()).To(MatchError(ContainSubstring("maxExportRows")))
		})

		// A default page a caller is not allowed to ask for is unreachable, so the
		// pair is refused rather than quietly narrowed to whichever is smaller.
		It("rejects a default page larger than the page a caller may ask for", func() {
			err := (&query.RowLimits{PageSize: 2000}).Validate()
			Expect(err).To(MatchError(ContainSubstring("pageSize 2000")))
			Expect(err).To(MatchError(ContainSubstring("maxPageSize 1000")))
		})
	})
})
