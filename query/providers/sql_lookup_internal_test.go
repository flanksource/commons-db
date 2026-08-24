package providers

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("buildLookupSQL", func() {
	regionBinding := query.ColumnFilterBinding{
		Column: "region", Key: "filter.region", Field: "region",
		Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true,
	}

	It("counts distinct values over the wrapped base", func() {
		statement, args, err := buildLookupSQL(dialectPostgres, ordersQuery, regionBinding, nil, "", 20)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(
			"WITH \"__cdb_base\" AS (\n" + ordersQuery + "\n)\n" +
				`SELECT "region" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total` + "\n" +
				`FROM "__cdb_base"` + "\n" +
				`WHERE "region" IS NOT NULL` + "\n" +
				`GROUP BY "region"` + "\n" +
				"ORDER BY 2 DESC, 1 ASC\n" +
				"LIMIT 20"))
		Expect(args).To(BeEmpty())
	})

	// The options offered must be the options the table can actually show, so
	// every other active selection still scopes the question.
	It("applies the sibling filters", func() {
		statement, args, err := buildLookupSQL(dialectPostgres, ordersQuery, regionBinding,
			[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "", 20)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`WHERE ("env" IN ($1) AND "region" IS NOT NULL)`))
		Expect(args).To(Equal([]any{"prod"}))
	})

	It("matches a search term case-insensitively", func() {
		statement, args, err := buildLookupSQL(dialectPostgres, ordersQuery, regionBinding, nil, "US", 20)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`LOWER("region") LIKE $1 ESCAPE '!'`))
		Expect(args).To(Equal([]any{"%us%"}))
	})

	// Someone typing "50%" is looking for a literal "50%", not for everything.
	DescribeTable("neutralises the wildcards in a typed search term",
		func(dialect sqlDialect, search, pattern string) {
			_, args, err := buildLookupSQL(dialect, ordersQuery, regionBinding, nil, search, 20)
			Expect(err).ToNot(HaveOccurred())
			Expect(args).To(Equal([]any{pattern}))
		},
		Entry("a percent", dialectPostgres, "50%", "%50!%%"),
		Entry("an underscore", dialectPostgres, "a_b", "%a!_b%"),
		Entry("the escape character itself", dialectPostgres, "a!b", "%a!!b%"),
		Entry("a bracket, which T-SQL reads as a class", dialectSQLServer, "a[b", `%a![b%`),
		Entry("a backslash escape on clickhouse", dialectClickHouse, "50%", `%50\%%`),
	)

	DescribeTable("takes the head of the set the way each dialect spells it",
		func(dialect sqlDialect, tail string) {
			statement, _, err := buildLookupSQL(dialect, ordersQuery, regionBinding, nil, "", 20)
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(HaveSuffix(tail))
		},
		Entry("postgres", dialectPostgres, "LIMIT 20"),
		Entry("mysql", dialectMySQL, "LIMIT 20"),
		Entry("clickhouse", dialectClickHouse, "LIMIT 20"),
		Entry("sqlserver", dialectSQLServer, "OFFSET 0 ROWS FETCH NEXT 20 ROWS ONLY"),
	)

	It("has no ESCAPE clause on clickhouse, whose LIKE takes none", func() {
		statement, _, err := buildLookupSQL(dialectClickHouse, ordersQuery, regionBinding, nil, "us", 20)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`lower("region") LIKE ?`))
		Expect(statement).ToNot(ContainSubstring("ESCAPE"))
	})

	// A range and a toggle are typed, not picked, so there is no list to offer.
	It("refuses a lookup on a filter with no values to list", func() {
		_, _, err := buildLookupSQL(dialectPostgres, ordersQuery, query.ColumnFilterBinding{
			Key: "filter.latency_ms", Field: "latency_ms", Kind: query.ColumnFilterKindRange,
		}, nil, "", 20)
		Expect(err).To(MatchError(ContainSubstring("has no values to list")))
	})
})
