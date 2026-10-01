package sqlite

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("normalizedCheckExpression", func() {
	DescribeTable("strips only parentheses that wrap the whole expression",
		func(expression, expected string) {
			Expect(normalizedCheckExpression(expression)).To(Equal(expected))
		},
		Entry("wrapped", "(a > 0)", "a > 0"),
		Entry("doubly wrapped", "((a > 0))", "a > 0"),
		Entry("two groups", "(a > 0) AND (b > 0)", "(a > 0) AND (b > 0)"),
		Entry("single-quoted parenthesis", "(name <> ')')", "name <> ')'"),
		Entry("double-quoted identifier with parenthesis", `("a)" > 0)`, `"a)" > 0`),
		Entry("backtick identifier with parenthesis", "(`a)` > 0)", "`a)` > 0"),
		Entry("escaped backtick inside identifier", "(`a``)` > 0)", "`a``)` > 0"),
		Entry("bracket identifier with parenthesis", "([a)] > 0)", "[a)] > 0"),
		Entry("bracket identifier with opening parenthesis", "([(a] > 0) AND (b > 0)", "([(a] > 0) AND (b > 0)"),
	)
})
