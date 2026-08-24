package query

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("column filter selection", func() {
	Describe("value selections", func() {
		DescribeTable("splits the wire form into kept and removed values",
			func(value any, include, exclude []string) {
				selection, err := parseColumnFilterSelection(ColumnFilterKindTerms, value)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.Include).To(Equal(include))
				Expect(selection.Exclude).To(Equal(exclude))
			},
			Entry("a comma-joined string", "us-east,us-west", []string{"us-east", "us-west"}, []string{}),
			Entry("an exclusion", "us-east,!eu", []string{"us-east"}, []string{"eu"}),
			Entry("a repeated query key", []string{"us-east", "!eu"}, []string{"us-east"}, []string{"eu"}),
			Entry("a decoded JSON list", []any{"us-east", "!eu"}, []string{"us-east"}, []string{"eu"}),
			Entry("padding and duplicates", " a , a ,!b , !b ", []string{"a"}, []string{"b"}),
		)

		// The grammar is dispatched from the column's kind, never sniffed from
		// the value. This is what lets one wire form carry every kind.
		It("keeps a comparison operator as a literal value", func() {
			selection, err := parseColumnFilterSelection(ColumnFilterKindTerms, ">=10")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Include).To(Equal([]string{">=10"}))
			Expect(selection.Range).To(BeNil())
		})

		It("rejects an exclusion with no value", func() {
			_, err := parseColumnFilterSelection(ColumnFilterKindTerms, "!")
			Expect(err).To(MatchError(ContainSubstring("excluded value must not be empty")))
		})

		It("rejects a value that is not a string", func() {
			_, err := parseColumnFilterSelection(ColumnFilterKindTerms, 42)
			Expect(err).To(MatchError(ContainSubstring("must be a string or string list")))
		})

		It("treats an empty selection as no selection", func() {
			selection, err := parseColumnFilterSelection(ColumnFilterKindTerms, " , ")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.IsZero()).To(BeTrue())
		})
	})

	Describe("numeric ranges", func() {
		DescribeTable("reads bounded tokens into one range",
			func(value string, min, max *FilterBound) {
				selection, err := parseColumnFilterSelection(ColumnFilterKindRange, value)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.Range).ToNot(BeNil())
				Expect(selection.Range.Min).To(Equal(min))
				Expect(selection.Range.Max).To(Equal(max))
			},
			Entry("both edges", ">=1024,<4096",
				&FilterBound{Value: float64(1024), Inclusive: true},
				&FilterBound{Value: float64(4096), Inclusive: false}),
			Entry("an open upper edge", ">=1024",
				&FilterBound{Value: float64(1024), Inclusive: true}, nil),
			Entry("an open lower edge", "<4096",
				nil, &FilterBound{Value: float64(4096), Inclusive: false}),
			Entry("exclusive and inclusive mixed", ">1024,<=4096",
				&FilterBound{Value: float64(1024), Inclusive: false},
				&FilterBound{Value: float64(4096), Inclusive: true}),
		)

		// A cursor fingerprints the resolved filters, so two spellings of one
		// bound must resolve to one value or a page would refuse to resume.
		It("normalizes a bound however it was written", func() {
			plain, err := parseColumnFilterSelection(ColumnFilterKindRange, ">=10")
			Expect(err).ToNot(HaveOccurred())
			decimal, err := parseColumnFilterSelection(ColumnFilterKindRange, ">=10.0")
			Expect(err).ToNot(HaveOccurred())
			Expect(plain.Range.Min.Value).To(Equal(decimal.Range.Min.Value))
		})

		DescribeTable("refuses a range it cannot apply",
			func(value, message string) {
				_, err := parseColumnFilterSelection(ColumnFilterKindRange, value)
				Expect(err).To(MatchError(ContainSubstring(message)))
			},
			Entry("no comparison operator", "1024", "needs a comparison operator"),
			Entry("a non-numeric bound", ">=abc", "is not a number"),
			Entry("two lower bounds", ">=1,>2", "two lower bounds"),
			Entry("two upper bounds", "<1,<=2", "two upper bounds"),
			Entry("an exclusion", "!>=1", "excluding a value is not supported"),
			Entry("an empty range", ">=4096,<=1024", "is above upper bound"),
		)
	})

	Describe("time ranges", func() {
		// Date math is resolved against the backend's clock, so rewriting it
		// here would pin a rolling window to the moment the request was parsed.
		It("carries date math through as written", func() {
			selection, err := parseColumnFilterSelection(ColumnFilterKindTime, ">=now-15m,<now")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Range.Min.Value).To(Equal("now-15m"))
			Expect(selection.Range.Max.Value).To(Equal("now"))
		})

		It("accepts an RFC3339 instant", func() {
			selection, err := parseColumnFilterSelection(ColumnFilterKindTime, ">=2024-01-02T03:04:05Z")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Range.Min.Value).To(Equal("2024-01-02T03:04:05Z"))
		})

		It("rejects a bound that is neither a time nor date math", func() {
			_, err := parseColumnFilterSelection(ColumnFilterKindTime, ">=yesterday")
			Expect(err).To(MatchError(ContainSubstring("is not an RFC3339 time or date math")))
		})
	})

	Describe("yes/no toggles", func() {
		DescribeTable("reads the selected arm",
			func(value string, expected bool) {
				selection, err := parseColumnFilterSelection(ColumnFilterKindBoolean, value)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.Bool).ToNot(BeNil())
				Expect(*selection.Bool).To(Equal(expected))
			},
			Entry("true", "true", true),
			Entry("false", "false", false),
			Entry("a negated true", "!true", false),
		)

		It("rejects two arms at once", func() {
			_, err := parseColumnFilterSelection(ColumnFilterKindBoolean, "true,false")
			Expect(err).To(MatchError(ContainSubstring("takes one value")))
		})

		It("rejects a value that is neither arm", func() {
			_, err := parseColumnFilterSelection(ColumnFilterKindBoolean, "maybe")
			Expect(err).To(MatchError(ContainSubstring("is not true or false")))
		})
	})

	// The browser decodes a selection into chips and re-encodes it on every
	// change, so a value it cannot round-trip would be corrupted by the act of
	// rendering it. This models that codec (split on comma, "!" excludes).
	Describe("the browser's codec", func() {
		DescribeTable("round-trips a selection byte for byte",
			func(value string) {
				Expect(reserializeLikeBrowser(value)).To(Equal(value))
			},
			Entry("a value selection", "us-east,!eu"),
			Entry("a numeric range", ">=1024,<4096"),
			Entry("a time range", ">=now-15m,<now"),
			Entry("a yes/no toggle", "true"),
		)
	})
})

// reserializeLikeBrowser models parseMultiFilterValue/serializeMultiFilterValue
// in clicky-ui's rpc/formMetadata.ts, which is the only codec a selection
// passes through between the filter bar and the query string.
func reserializeLikeBrowser(value string) string {
	parts := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if after, negated := strings.CutPrefix(item, "!"); negated {
			parts = append(parts, "!"+after)
			continue
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, ",")
}
