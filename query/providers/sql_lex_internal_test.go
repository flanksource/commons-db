package providers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("splitWithPrefix", func() {
	It("reports no prefix for a plain statement", func() {
		prefix, body, names, err := splitWithPrefix("SELECT 1")
		Expect(err).ToNot(HaveOccurred())
		Expect(prefix).To(BeEmpty())
		Expect(body).To(Equal("SELECT 1"))
		Expect(names).To(BeEmpty())
	})

	It("separates one CTE from the statement it introduces", func() {
		prefix, body, names, err := splitWithPrefix("WITH a AS (SELECT 1) SELECT * FROM a")
		Expect(err).ToNot(HaveOccurred())
		Expect(prefix).To(Equal("WITH a AS (SELECT 1)"))
		Expect(body).To(Equal("SELECT * FROM a"))
		Expect(names).To(Equal([]string{"a"}))
	})

	It("separates several CTEs", func() {
		prefix, body, names, err := splitWithPrefix("WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM b")
		Expect(err).ToNot(HaveOccurred())
		Expect(prefix).To(Equal("WITH a AS (SELECT 1), b AS (SELECT 2)"))
		Expect(body).To(Equal("SELECT * FROM b"))
		Expect(names).To(Equal([]string{"a", "b"}))
	})

	It("walks past parentheses inside a CTE body", func() {
		_, body, names, err := splitWithPrefix(
			"WITH a AS (SELECT (SELECT max(id) FROM t) AS n) SELECT * FROM a")
		Expect(err).ToNot(HaveOccurred())
		Expect(body).To(Equal("SELECT * FROM a"))
		Expect(names).To(Equal([]string{"a"}))
	})

	It("keeps RECURSIVE on the prefix", func() {
		prefix, _, names, err := splitWithPrefix("WITH RECURSIVE t AS (SELECT 1) SELECT * FROM t")
		Expect(err).ToNot(HaveOccurred())
		Expect(prefix).To(HavePrefix("WITH RECURSIVE"))
		Expect(names).To(Equal([]string{"t"}))
	})

	// A statement that merely mentions "with" is not a CTE list, and reading one
	// there would cut the query in the wrong place.
	DescribeTable("ignores a WITH that is not a CTE clause",
		func(statement string) {
			prefix, body, _, err := splitWithPrefix(statement)
			Expect(err).ToNot(HaveOccurred())
			Expect(prefix).To(BeEmpty())
			Expect(body).To(Equal(statement))
		},
		Entry("inside a string literal", "SELECT 'with a AS (x)' AS note"),
		Entry("inside a line comment", "-- with a AS (x)\nSELECT 1"),
		Entry("inside a block comment", "/* with a AS (x) */ SELECT 1"),
	)
})

var _ = Describe("assertNoPlaceholders", func() {
	// Postgres reads "?" as a jsonb operator, and the author's statement never
	// passes through a placeholder rewriter, so it is left alone.
	DescribeTable("accepts a postgres statement whose ? is not a bind marker",
		func(statement string) {
			Expect(assertNoPlaceholders(dialectPostgres, statement)).To(Succeed())
		},
		Entry("a jsonb key test", `SELECT * FROM t WHERE payload ? 'k'`),
		Entry("a jsonb any-key test", `SELECT * FROM t WHERE payload ?| array['a','b']`),
		Entry("a question mark in a literal", `SELECT 'huh?' AS note`),
	)

	// A driver numbers placeholders across the whole statement, so the wrapper's
	// values would bind to the author's marker instead of its own.
	DescribeTable("refuses a statement that already binds",
		func(dialect sqlDialect, statement string) {
			Expect(assertNoPlaceholders(dialect, statement)).To(
				MatchError(ContainSubstring("already contains a bind placeholder")))
		},
		Entry("postgres $1", dialectPostgres, "SELECT * FROM t WHERE id = $1"),
		Entry("mysql ?", dialectMySQL, "SELECT * FROM t WHERE id = ?"),
		Entry("sqlserver @p1", dialectSQLServer, "SELECT * FROM t WHERE id = @p1"),
		Entry("sqlserver ?", dialectSQLServer, "SELECT * FROM t WHERE id = ?"),
		Entry("clickhouse ?", dialectClickHouse, "SELECT * FROM t WHERE id = ?"),
	)

	It("accepts a mysql question mark inside a literal", func() {
		Expect(assertNoPlaceholders(dialectMySQL, `SELECT 'huh?' AS note`)).To(Succeed())
	})

	// ClickHouse substitutes client-side by regex over the raw text with no idea
	// which markers are inside a literal, so the guard must read it the same way
	// or it would disagree with the driver about what a marker is.
	It("refuses a clickhouse question mark even inside a literal", func() {
		Expect(assertNoPlaceholders(dialectClickHouse, `SELECT 'huh?' AS note`)).To(
			MatchError(ContainSubstring("already contains a bind placeholder")))
	})

	It("treats a dollar-quoted body as a literal", func() {
		Expect(assertNoPlaceholders(dialectPostgres, `SELECT $$a $1 b$$ AS note`)).To(Succeed())
		Expect(assertNoPlaceholders(dialectPostgres, `SELECT $tag$a $1 b$tag$ AS note`)).To(Succeed())
	})
})
