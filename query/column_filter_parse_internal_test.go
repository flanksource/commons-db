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
				selection, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection(value)
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
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection(">=10")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Include).To(Equal([]string{">=10"}))
			Expect(selection.Range).To(BeNil())
		})

		It("rejects an exclusion with no value", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection("!")
			Expect(err).To(MatchError(ContainSubstring("excluded value must not be empty")))
		})

		It("rejects a value that is not a string", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection(42)
			Expect(err).To(MatchError(ContainSubstring("must be a string or string list")))
		})

		It("treats an empty selection as no selection", func() {
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection(" , ")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.IsZero()).To(BeTrue())
		})
	})

	Describe("numeric ranges", func() {
		DescribeTable("reads bounded tokens into one range",
			func(value string, min, max *FilterBound) {
				selection, err := ColumnFilterBinding{Kind: ColumnFilterKindRange}.parseSelection(value)
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
			plain, err := ColumnFilterBinding{Kind: ColumnFilterKindRange}.parseSelection(">=10")
			Expect(err).ToNot(HaveOccurred())
			decimal, err := ColumnFilterBinding{Kind: ColumnFilterKindRange}.parseSelection(">=10.0")
			Expect(err).ToNot(HaveOccurred())
			Expect(plain.Range.Min.Value).To(Equal(decimal.Range.Min.Value))
		})

		DescribeTable("refuses a range it cannot apply",
			func(value, message string) {
				_, err := ColumnFilterBinding{Kind: ColumnFilterKindRange}.parseSelection(value)
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

	Describe("exact matches", func() {
		// An exact match is a value selection that is typed rather than picked,
		// so it must read the same grammar or a UUID column would stop
		// understanding the request it understood as terms.
		It("splits the wire form exactly as a value selection does", func() {
			exact, err := ColumnFilterBinding{Kind: ColumnFilterKindExact}.parseSelection("a,b,!c")
			Expect(err).ToNot(HaveOccurred())
			terms, err := ColumnFilterBinding{Kind: ColumnFilterKindTerms}.parseSelection("a,b,!c")
			Expect(err).ToNot(HaveOccurred())
			Expect(exact.Include).To(Equal(terms.Include))
			Expect(exact.Exclude).To(Equal(terms.Exclude))
		})

		It("keeps a comparison operator as a literal value", func() {
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindExact}.parseSelection(">=10")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Include).To(Equal([]string{">=10"}))
			Expect(selection.Range).To(BeNil())
		})
	})

	Describe("duration ranges", func() {
		DescribeTable("resolves a duration into the unit the column stores",
			func(unit, value string, expected float64) {
				selection, err := ColumnFilterBinding{
					Kind: ColumnFilterKindDuration, Unit: unit,
				}.parseSelection(value)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.Range.Min.Value).To(Equal(expected))
			},
			Entry("an unset unit is milliseconds", "", ">=5s", float64(5000)),
			Entry("milliseconds", "ms", ">=5s", float64(5000)),
			Entry("seconds", "s", ">=5s", float64(5)),
			Entry("a compound duration", "", ">=2m30s", float64(150000)),
			Entry("a sub-unit duration keeps its precision", "", ">=500us", 0.5),
			Entry("milliseconds against a seconds column", "s", ">=1500ms", 1.5),
		)

		It("bounds the upper edge too", func() {
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindDuration}.parseSelection("<1h")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Range.Max.Value).To(Equal(float64(3600000)))
		})

		// Duration columns filtered by range before this kind existed, so a bare
		// number must still mean the number it always did. The numeric parse is
		// the reference rather than a literal, which is the actual guarantee.
		It("takes a bare number as already being in the column's unit", func() {
			for _, unit := range []string{"", "ms", "s"} {
				duration, err := ColumnFilterBinding{
					Kind: ColumnFilterKindDuration, Unit: unit,
				}.parseSelection(">=5000")
				Expect(err).ToNot(HaveOccurred())
				numeric, err := ColumnFilterBinding{Kind: ColumnFilterKindRange}.parseSelection(">=5000")
				Expect(err).ToNot(HaveOccurred())
				Expect(duration.Range.Min.Value).To(Equal(numeric.Range.Min.Value))
			}
		})

		// Duration bounds are float64, so the existing ordering check covers them.
		It("refuses a range that can never match", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindDuration}.parseSelection(">=1h,<=1m")
			Expect(err).To(MatchError(ContainSubstring("is above upper bound")))
		})

		It("rejects an operand that is neither a duration nor a number", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindDuration}.parseSelection(">=5 seconds")
			Expect(err).To(MatchError(ContainSubstring("is not a duration")))
		})

		It("rejects a unit no duration can be expressed in", func() {
			_, err := ColumnFilterBinding{
				Kind: ColumnFilterKindDuration, Unit: "bytes",
			}.parseSelection(">=5s")
			Expect(err).To(MatchError(ContainSubstring("needs a time unit")))
		})
	})

	Describe("date ranges", func() {
		// A date bound differs from a time bound in the control it renders, not
		// in what it accepts, so the two parses must agree exactly.
		DescribeTable("reads the same operands a time range does",
			func(value string) {
				date, err := ColumnFilterBinding{Kind: ColumnFilterKindDate}.parseSelection(value)
				Expect(err).ToNot(HaveOccurred())
				moment, err := ColumnFilterBinding{Kind: ColumnFilterKindTime}.parseSelection(value)
				Expect(err).ToNot(HaveOccurred())
				Expect(date.Range).To(Equal(moment.Range))
			},
			Entry("date math", ">=now-7d,<now"),
			Entry("an RFC3339 instant", ">=2024-01-02T03:04:05Z"),
			Entry("a calendar day", ">=2024-01-02"),
		)

		It("rejects a bound that is neither a date nor date math", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindDate}.parseSelection(">=yesterday")
			Expect(err).To(MatchError(ContainSubstring("is not an RFC3339 time or date math")))
		})
	})

	Describe("time ranges", func() {
		// Date math is resolved against the backend's clock, so rewriting it
		// here would pin a rolling window to the moment the request was parsed.
		It("carries date math through as written", func() {
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindTime}.parseSelection(">=now-15m,<now")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Range.Min.Value).To(Equal("now-15m"))
			Expect(selection.Range.Max.Value).To(Equal("now"))
		})

		It("accepts an RFC3339 instant", func() {
			selection, err := ColumnFilterBinding{Kind: ColumnFilterKindTime}.parseSelection(">=2024-01-02T03:04:05Z")
			Expect(err).ToNot(HaveOccurred())
			Expect(selection.Range.Min.Value).To(Equal("2024-01-02T03:04:05Z"))
		})

		It("rejects a bound that is neither a time nor date math", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindTime}.parseSelection(">=yesterday")
			Expect(err).To(MatchError(ContainSubstring("is not an RFC3339 time or date math")))
		})
	})

	Describe("yes/no toggles", func() {
		DescribeTable("reads the selected arm",
			func(value string, expected bool) {
				selection, err := ColumnFilterBinding{Kind: ColumnFilterKindBoolean}.parseSelection(value)
				Expect(err).ToNot(HaveOccurred())
				Expect(selection.Bool).ToNot(BeNil())
				Expect(*selection.Bool).To(Equal(expected))
			},
			Entry("true", "true", true),
			Entry("false", "false", false),
			Entry("a negated true", "!true", false),
		)

		It("rejects two arms at once", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindBoolean}.parseSelection("true,false")
			Expect(err).To(MatchError(ContainSubstring("takes one value")))
		})

		It("rejects a value that is neither arm", func() {
			_, err := ColumnFilterBinding{Kind: ColumnFilterKindBoolean}.parseSelection("maybe")
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
			Entry("a duration range", ">=500ms,<5s"),
			Entry("a time range", ">=now-15m,<now"),
			Entry("a date range", ">=now-7d,<now"),
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
