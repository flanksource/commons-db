package esdsl

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("EscapeLucene", func() {
	DescribeTable("neutralises query-string syntax",
		func(input, expected string) {
			Expect(EscapeLucene(input)).To(Equal(expected))
		},
		Entry("boolean operators", `a && b || c`, `a \&\& b \|\| c`),
		Entry("wildcards", `web-*`, `web\-\*`),
		Entry("field qualifier", `level:error`, `level\:error`),
		Entry("grouping", `(a OR b)`, `\(a OR b\)`),
		Entry("quotes and backslash", `he said "hi"\`, `he said \"hi\"\\`),
		Entry("plain text is untouched", `connection refused`, `connection refused`),
		Entry("non-ascii is untouched", `größe`, `größe`),
	)

	It("escapes a parameter-sourced query_string operand", func() {
		query := compileQuery(
			Condition{Op: OpQueryString, Fields: []string{"message"}, Value: Param("q")},
			ParamBinding{Name: "q", Value: `a OR b:*`},
		)
		Expect(query["query_string"].(map[string]any)["query"]).To(Equal(`a OR b\:\*`))
	})

	It("leaves an authored literal alone so hand-written syntax still works", func() {
		query := compileQuery(Condition{
			Op: OpQueryString, Fields: []string{"message"}, Value: Literal(`level:error AND host:web*`),
		})
		Expect(query["query_string"].(map[string]any)["query"]).To(Equal(`level:error AND host:web*`))
	})

	It("honours an explicit escape opt-out", func() {
		query := compileQuery(
			Condition{Op: OpQueryString, Fields: []string{"message"}, Value: Param("q"), Escape: boolPtr(false)},
			ParamBinding{Name: "q", Value: `level:error`},
		)
		Expect(query["query_string"].(map[string]any)["query"]).To(Equal(`level:error`))
	})
})

var _ = Describe("ValidateFieldName", func() {
	DescribeTable("accepts a real field name",
		func(name string) { Expect(ValidateFieldName(name)).To(Succeed()) },
		Entry("metadata field", "@timestamp"),
		Entry("dotted path", "resource.attributes.service-name"),
		Entry("wildcard suffix", "kubernetes.labels.*"),
		Entry("bare wildcard, as stored_fields uses", "*"),
		Entry("underscore prefix", "_id"),
	)

	DescribeTable("rejects anything that could break out of the body",
		func(name string) {
			Expect(ValidateFieldName(name)).To(MatchError(ContainSubstring("is not a valid OpenSearch field")))
		},
		Entry("json break-out", `foo"}`),
		Entry("template expression", "{{x}}"),
		Entry("leading digit", "1field"),
		Entry("whitespace", "service name"),
		Entry("nested json", `a":{"b`),
	)

	It("rejects an empty field name", func() {
		Expect(ValidateFieldName("")).To(MatchError(ContainSubstring("must not be empty")))
	})
})

var _ = Describe("regexp guard", func() {
	It("always bounds the automaton", func() {
		query := compileQuery(Condition{Op: OpRegexp, Field: "host", Value: Literal("web-[0-9]+")})
		Expect(query["regexp"].(map[string]any)["host"]).To(HaveKeyWithValue("max_determinized_states", maxDeterminizedStates))
	})

	It("rejects a pattern beyond the length cap", func() {
		_, err := Compile(CompileRequest{
			Search: Search{Query: &Condition{Op: OpRegexp, Field: "host", Value: Param("pattern")}},
			Params: []ParamBinding{{Name: "pattern", Value: strings.Repeat("a", maxRegexpLength+1)}},
		})
		Expect(err).To(MatchError(ContainSubstring("regexp is 1001 characters, the limit is 1000")))
	})
})
