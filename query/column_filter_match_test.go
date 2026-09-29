// Specs for the match filter kind: MatchItem patterns, parsed with the terms
// grammar and read the way collections.MatchItem reads them.
package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = Describe("match filter", func() {
	DescribeTable("reads a pattern as MatchItem does, with * special only at its edges",
		func(pattern string, expected query.MatchPattern) {
			Expect(query.ParseMatchPattern(pattern)).To(Equal(expected))
		},
		Entry("a lone star matches anything", "*", query.MatchPattern{Mode: query.MatchAny}),
		Entry("a plain value matches whole", "Foo", query.MatchPattern{Mode: query.MatchExact, Text: "Foo"}),
		Entry("a trailing star matches a prefix", "api*", query.MatchPattern{Mode: query.MatchPrefix, Text: "api"}),
		Entry("a leading star matches a suffix", "*-canary", query.MatchPattern{Mode: query.MatchSuffix, Text: "-canary"}),
		Entry("stars at both edges match a substring", "*err*", query.MatchPattern{Mode: query.MatchContains, Text: "err"}),
		Entry("a star inside is literal", "a*b", query.MatchPattern{Mode: query.MatchExact, Text: "a*b"}),
		Entry("two stars match any substring at all", "**", query.MatchPattern{Mode: query.MatchContains, Text: ""}),
		Entry("SQL wildcards are literal", "50%", query.MatchPattern{Mode: query.MatchExact, Text: "50%"}),
	)

	It("is a value selection the backend can enumerate, offered as a multi-filter", func() {
		kind := query.ColumnFilterKindMatch
		Expect(kind.Valid()).To(BeTrue())
		Expect(kind.Lookupable()).To(BeTrue())
		Expect(kind.ControlType()).To(Equal("multi-filter"))
		Expect(kind.CompilesAs()).To(Equal(query.ColumnFilterKindMatch))
		Expect(query.ColumnFilterKindValues()).To(ContainElement(string(query.ColumnFilterKindMatch)))
	})

	It("parses its value with the terms grammar, a ! prefix excluding", func() {
		value, err := query.ColumnFilterBinding{Kind: query.ColumnFilterKindMatch}.ParseSelection("api*,!*-canary")
		Expect(err).ToNot(HaveOccurred())
		Expect(value.Kind).To(Equal(query.ColumnFilterKindMatch))
		Expect(value.Include).To(Equal([]string{"api*"}))
		Expect(value.Exclude).To(Equal([]string{"*-canary"}))
	})
})
