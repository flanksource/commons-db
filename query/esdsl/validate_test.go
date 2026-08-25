package esdsl

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Search.Validate", func() {
	DescribeTable("rejects a malformed condition",
		func(condition Condition, expected string) {
			Expect(Search{Query: &condition}.Validate()).To(MatchError(ContainSubstring(expected)))
		},
		Entry("unknown operator",
			Condition{Op: "contains", Field: "message", Value: Literal("x")},
			`unknown operator "contains"`),
		Entry("unknown occur",
			Condition{Op: OpTerm, Occur: "maybe", Field: "level", Value: Literal("x")},
			`unknown occur "maybe"`),
		Entry("term without a field",
			Condition{Op: OpTerm, Value: Literal("error")},
			`operator "term" requires a field`),
		Entry("term given a field list",
			Condition{Op: OpTerm, Fields: []string{"a", "b"}, Value: Literal("error")},
			`operator "term" takes field, not fields`),
		Entry("multi_match without any field",
			Condition{Op: OpMultiMatch, Value: Literal("boom")},
			`operator "multi_match" requires field or fields`),
		Entry("exists given a value",
			Condition{Op: OpExists, Field: "trace_id", Value: Literal("x")},
			`operator "exists" does not take a value`),
		Entry("terms with both value and values",
			Condition{Op: OpTerms, Field: "level", Value: Literal("error"), Values: []Value{{Literal: "warn"}}},
			`operator "terms" takes value or values, not both`),
		Entry("terms with no operand",
			Condition{Op: OpTerms, Field: "level"},
			`operator "terms" requires values`),
		Entry("match without a value",
			Condition{Op: OpMatch, Field: "message"},
			`operator "match" requires a value`),
		Entry("range with no bound",
			Condition{Op: OpRange, Field: "@timestamp"},
			`operator "range" requires at least one of gt, gte, lt, lte`),
		Entry("range given a value",
			Condition{Op: OpRange, Field: "@timestamp", Value: Literal("now")},
			`operator "range" takes range bounds, not a value`),
		Entry("term given a range bound",
			Condition{Op: OpTerm, Field: "level", Value: Literal("error"), Gte: Literal("a")},
			`operator "term" does not take range bounds`),
		Entry("nested without a path",
			Condition{Op: OpNested, Conditions: []Condition{{Op: OpTerm, Field: "tags.key", Value: Literal("env")}}},
			"nested requires a path"),
		Entry("nested without children",
			Condition{Op: OpNested, Path: "tags"},
			"nested requires at least one condition"),
		Entry("leaf given children",
			Condition{Op: OpTerm, Field: "level", Value: Literal("error"), Conditions: []Condition{{Op: OpMatchAll}}},
			`operator "term" does not take child conditions`),
		Entry("invalid field name",
			Condition{Op: OpTerm, Field: `foo"}`, Value: Literal("error")},
			`field name "foo\"}" is not a valid OpenSearch field`),
		Entry("nested error reports its path",
			Condition{Op: OpBool, Conditions: []Condition{{Op: OpTerm, Value: Literal("error")}}},
			"query.conditions[0]"),
	)

	DescribeTable("rejects a qualifier the operator would silently drop",
		func(condition Condition, expected string) {
			Expect(Search{Query: &condition}.Validate()).To(MatchError(ContainSubstring(expected)))
		},
		Entry("slop on term",
			Condition{Op: OpTerm, Field: "level", Value: Literal("error"), Slop: intPtr(2)},
			`slop is not supported by operator "term"`),
		Entry("caseInsensitive on match",
			Condition{Op: OpMatch, Field: "message", Value: Literal("boom"), CaseInsensitive: boolPtr(true)},
			`caseInsensitive is not supported by operator "match"`),
		Entry("format on term",
			Condition{Op: OpTerm, Field: "@timestamp", Value: Literal("now"), Format: "epoch_millis"},
			`format is not supported by operator "term"`),
		Entry("escape on match",
			Condition{Op: OpMatch, Field: "message", Value: Literal("boom"), Escape: boolPtr(false)},
			`escape is not supported by operator "match"`),
		Entry("scoreMode on bool",
			Condition{Op: OpBool, ScoreMode: "avg", Conditions: []Condition{{Op: OpMatchAll}}},
			`scoreMode is not supported by operator "bool"`),
		Entry("fuzziness on wildcard",
			Condition{Op: OpWildcard, Field: "host", Value: Literal("web-*"), Fuzziness: "AUTO"},
			`fuzziness is not supported by operator "wildcard"`),
	)

	DescribeTable("rejects malformed output shaping",
		func(search Search, expected string) {
			Expect(search.Validate()).To(MatchError(ContainSubstring(expected)))
		},
		Entry("sort without a field",
			Search{Sort: []SortBy{{Order: "desc"}}},
			"sort[0]: field is required"),
		Entry("sort with an unknown order",
			Search{Sort: []SortBy{{Field: "@timestamp", Order: "descending"}}},
			`sort[0]: order must be asc or desc, got "descending"`),
		Entry("negative size", Search{Size: intPtr(-1)}, "size must not be negative"),
		Entry("negative from", Search{From: intPtr(-1)}, "from must not be negative"),
		Entry("invalid _source include",
			Search{Source: &Source{Includes: []string{`bad"`}}},
			"source.includes:"),
		Entry("invalid time field", Search{TimeField: "{{now}}"}, "timeField:"),
		Entry("time format without field",
			Search{TimeFieldFormat: TimeFieldFormatEpochMillis},
			"timeFieldFormat requires timeField"),
		Entry("unknown time format",
			Search{TimeField: "observed", TimeFieldFormat: "unix"},
			`unknown timeFieldFormat "unix"`),
	)

	It("accepts _score and _doc as sort fields", func() {
		Expect(Search{Sort: []SortBy{{Field: "_score", Order: "desc"}, {Field: "_doc"}}}.Validate()).To(Succeed())
	})

	It("accepts an empty specification", func() {
		Expect(Search{}.Validate()).To(Succeed())
	})
})

var _ = Describe("Operator catalog", func() {
	It("describes every operator exactly once", func() {
		seen := map[Operator]bool{}
		for _, info := range Catalog() {
			Expect(seen[info.Op]).To(BeFalse(), "duplicate catalog entry for %q", info.Op)
			seen[info.Op] = true
			Expect(info.Label).ToNot(BeEmpty(), "operator %q has no label", info.Op)
			Expect(info.FieldTypes).ToNot(BeEmpty(), "operator %q has no field types", info.Op)
			Expect(info.NeedsField && info.AcceptsFields).To(BeFalse(),
				"operator %q cannot both require one field and accept many", info.Op)
		}
		Expect(Operators()).To(HaveLen(len(seen)))
	})

	It("compiles every non-group operator the catalog advertises", func() {
		for _, info := range Catalog() {
			if info.Group {
				continue
			}
			node := bound{spec: Condition{Op: info.Op, Field: "field"}}
			switch info.Arity {
			case AritySingle:
				node.value = &boundValue{value: "x"}
			case ArityMultiple:
				node.values = []boundValue{{value: "x"}}
			case ArityRange:
				node.gte = &boundValue{value: "x"}
			}
			clause, err := compileLeaf(node, "query")
			Expect(err).ToNot(HaveOccurred(), "operator %q has no compiler", info.Op)
			Expect(clause).To(HaveKey(string(info.Op)), "operator %q emits the wrong clause", info.Op)
		}
	})
})
