package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("column filter limits", func() {
	profileWith := func(filter *query.ColumnFilterDef) query.Profile {
		return query.Profile{
			Name:     "orders",
			Provider: query.ProviderConfig{Type: "postgres"},
			Query:    "select tenant from orders",
			Columns:  []query.ColumnDef{{Name: "tenant", Type: query.ColumnTypeString, Filter: filter}},
		}
	}

	Describe("validation", func() {
		// A limit outside the range is rejected where it is written rather than
		// quietly reduced at request time, so the author learns the ceiling from
		// the error instead of from a shorter list than they asked for.
		DescribeTable("rejects a limit no lookup could honour",
			func(def query.ColumnFilterDef, expected string) {
				Expect(def.Validate("tenant")).To(MatchError(ContainSubstring(expected)))
			},
			Entry("zero", query.ColumnFilterDef{Limit: lo.ToPtr(0)}, "out of range"),
			Entry("negative", query.ColumnFilterDef{Limit: lo.ToPtr(-1)}, "out of range"),
			Entry("above the ceiling",
				query.ColumnFilterDef{Limit: lo.ToPtr(query.MaxFilterLookupLimit + 1)}, "out of range"),
			Entry("on a range filter, which has no list to cap",
				query.ColumnFilterDef{Kind: query.ColumnFilterKindRange, Limit: lo.ToPtr(10)},
				`requires a "terms" filter`),
		)

		DescribeTable("accepts a limit a lookup can serve",
			func(def query.ColumnFilterDef) { Expect(def.Validate("tenant")).To(Succeed()) },
			Entry("the smallest", query.ColumnFilterDef{Limit: lo.ToPtr(1)}),
			Entry("the ceiling", query.ColumnFilterDef{Limit: lo.ToPtr(query.MaxFilterLookupLimit)}),
			Entry("explicitly a value selection",
				query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Limit: lo.ToPtr(25)}),
			Entry("undeclared", query.ColumnFilterDef{}),
		)
	})

	It("carries a declared limit onto the binding every consumer reads", func() {
		bindings, err := profileWith(&query.ColumnFilterDef{Limit: lo.ToPtr(12)}).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
			Column: "tenant", Key: "filter.tenant", Field: "tenant", Label: "tenant",
			Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true, Limit: 12,
		}}))
	})

	// Zero is not "no values" — it is "whoever asks decides", which is what lets
	// a caller with its own sizing (the connection browser) keep choosing.
	It("leaves the limit unset on a binding nobody declared one for", func() {
		bindings, err := profileWith(nil).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(HaveLen(1))
		Expect(bindings[0].Limit).To(BeZero())
	})

	It("defaults to a head small enough to read", func() {
		Expect(query.DefaultFilterLookupLimit).To(And(BeNumerically(">", 0),
			BeNumerically("<=", query.MaxFilterLookupLimit)))
	})
})
