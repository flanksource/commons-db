package sqlite

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("sqlProvider.LookupFilterValues", func() {
	DescribeTable("refuses a limit outside 1..MaxFilterLookupLimit before connecting", func(limit int, message string) {
		_, _, err := sqlProvider{}.LookupFilterValues(context.New(), query.ProviderRequest{Query: ordersQuery}, query.ColumnFilterBinding{}, "", limit)
		Expect(err).To(MatchError(ContainSubstring(message)))
	},
		Entry("zero", 0, "positive limit"),
		Entry("above the ceiling", query.MaxFilterLookupLimit+1, "at most"),
	)
})

// lookupSQL is the distinct-values statement for binding over base.
func lookupSQL(dialect sqlDialect, base sqlQuery, binding query.ColumnFilterBinding,
	siblings []query.ColumnFilterValue, search string) (string, []any, error) {
	return rendered(buildLookupSQL(dialect, sqlLookupRequest{
		Query: base, Binding: binding, Siblings: siblings, Search: search, Limit: 20,
	}))
}

var _ = Describe("buildLookupSQL", func() {
	orders := sqlQuery{Text: ordersQuery}
	regionBinding := query.ColumnFilterBinding{
		Column: "region", Key: "filter.region", Field: "region",
		Kind: query.ColumnFilterKindTerms, Lookup: true, Multi: true,
	}

	It("counts distinct values over the wrapped base", func() {
		statement, args, err := lookupSQL(dialectPostgres, orders, regionBinding, nil, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(basePrefix(`"__cdb_base"`) +
			`SELECT "region" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total FROM "__cdb_base" ` +
			`WHERE NOT "region" IS NULL GROUP BY "region" ORDER BY 2 DESC, 1 ASC LIMIT 20`))
		Expect(args).To(BeEmpty())
	})

	// The options offered must be the options the table can actually show, so
	// every other active selection still scopes the question.
	It("applies the sibling filters", func() {
		statement, args, err := lookupSQL(dialectPostgres, orders, regionBinding,
			[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "")
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`WHERE ("env" IN ($1) AND NOT "region" IS NULL)`))
		Expect(args).To(Equal([]any{"prod"}))
	})

	It("matches a search term case-insensitively", func() {
		statement, args, err := lookupSQL(dialectPostgres, orders, regionBinding, nil, "US")
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`LOWER("region") LIKE $1 ESCAPE '!'`))
		Expect(args).To(Equal([]any{"%us%"}))
	})

	// Someone typing "50%" is looking for a literal "50%", not for everything.
	DescribeTable("neutralises the wildcards in a typed search term",
		func(dialect sqlDialect, search, pattern string) {
			_, args, err := lookupSQL(dialect, orders, regionBinding, nil, search)
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
			statement, _, err := lookupSQL(dialect, orders, regionBinding, nil, "")
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(HaveSuffix(tail))
		},
		Entry("postgres", dialectPostgres, "LIMIT 20"),
		Entry("mysql", dialectMySQL, "LIMIT 20"),
		Entry("clickhouse", dialectClickHouse, "LIMIT 20"),
		Entry("sqlserver", dialectSQLServer, "OFFSET 0 ROWS FETCH NEXT 20 ROWS ONLY"),
	)

	It("has no ESCAPE clause on clickhouse, whose LIKE takes none", func() {
		statement, _, err := lookupSQL(dialectClickHouse, orders, regionBinding, nil, "us")
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`LOWER("region") LIKE @cdb_p1`))
		Expect(statement).ToNot(ContainSubstring("ESCAPE"))
	})

	Describe("an array column", func() {
		tablesBinding := query.ColumnFilterBinding{
			Column: "tables", Key: "filter.tables", Field: "tables",
			Kind: query.ColumnFilterKindTerms, Array: true, Lookup: true, Multi: true,
		}

		// Each element is a value, counted once per row that holds it — however
		// many times that row's array repeats it.
		DescribeTable("lists string array members with row counts in every SQL dialect",
			func(dialect sqlDialect, expected string) {
				statement, args, err := lookupSQL(dialect, orders, tablesBinding,
					[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "Pol")
				Expect(err).ToNot(HaveOccurred())
				Expect(statement).To(Equal(expected))
				Expect(args).To(Equal([]any{"prod", "%pol%"}))
			},
			Entry("sqlite JSON", dialectSQLite, basePrefix(`"__cdb_base"`)+
				`SELECT "__cdb_item".value AS value, COUNT(DISTINCT "__cdb_rows"."__cdb_row") AS count, COUNT(*) OVER () AS total `+
				`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS "__cdb_row", "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN (?)) AS "__cdb_rows" CROSS JOIN JSON_EACH("__cdb_rows"."__cdb_array") AS "__cdb_item" `+
				`WHERE ("__cdb_item".type = 'text' AND NOT "__cdb_item".value IS NULL AND LOWER("__cdb_item".value) LIKE ? ESCAPE '!') `+
				`GROUP BY "__cdb_item".value ORDER BY 2 DESC, 1 ASC LIMIT 20`),
			Entry("postgres native arrays, json, and jsonb", dialectPostgres, basePrefix(`"__cdb_base"`)+
				`SELECT ("__cdb_item"."__cdb_value" #>> '{}') AS value, COUNT(DISTINCT "__cdb_rows"."__cdb_row") AS count, COUNT(*) OVER () AS total `+
				`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS "__cdb_row", "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN ($1)) AS "__cdb_rows" CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS(COALESCE(NULLIF(TO_JSONB("__cdb_rows"."__cdb_array"), CAST('null' AS JSONB)), CAST('[]' AS JSONB))) AS "__cdb_item"("__cdb_value") `+
				`WHERE (JSONB_TYPEOF("__cdb_item"."__cdb_value") = 'string' AND NOT ("__cdb_item"."__cdb_value" #>> '{}') IS NULL AND LOWER(("__cdb_item"."__cdb_value" #>> '{}')) LIKE $2 ESCAPE '!') `+
				`GROUP BY ("__cdb_item"."__cdb_value" #>> '{}') ORDER BY 2 DESC, 1 ASC LIMIT 20`),
			Entry("mysql JSON", dialectMySQL, basePrefix("`__cdb_base`")+
				"SELECT JSON_UNQUOTE(`__cdb_item`.`__cdb_value`) AS value, COUNT(DISTINCT `__cdb_rows`.`__cdb_row`) AS count, COUNT(*) OVER () AS total "+
				"FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS `__cdb_row`, `tables` AS `__cdb_array` FROM `__cdb_base` WHERE `env` IN (?)) AS `__cdb_rows` CROSS JOIN JSON_TABLE(CASE WHEN JSON_TYPE(`__cdb_rows`.`__cdb_array`) = 'ARRAY' THEN `__cdb_rows`.`__cdb_array` ELSE JSON_ARRAY() END, '$[*]' COLUMNS(`__cdb_value` JSON PATH '$')) AS `__cdb_item` "+
				"WHERE (JSON_TYPE(`__cdb_item`.`__cdb_value`) = 'STRING' AND NOT JSON_UNQUOTE(`__cdb_item`.`__cdb_value`) IS NULL AND LOWER(JSON_UNQUOTE(`__cdb_item`.`__cdb_value`)) LIKE ? ESCAPE '!') "+
				"GROUP BY JSON_UNQUOTE(`__cdb_item`.`__cdb_value`) ORDER BY 2 DESC, 1 ASC LIMIT 20"),
			Entry("sqlserver JSON text and ntext", dialectSQLServer,
				"WITH [__cdb_base] AS (SELECT id AS id, region AS region, env AS env, latency_ms AS latency_ms FROM orders) "+
					`SELECT [__cdb_item].[value] AS value, COUNT(DISTINCT [__cdb_rows].[__cdb_row]) AS count, COUNT(*) OVER () AS total `+
					`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS [__cdb_row], [tables] AS [__cdb_array] FROM [__cdb_base] WHERE [env] IN (@p1)) AS [__cdb_rows] CROSS APPLY OPENJSON(COALESCE(CONVERT(NVARCHAR(MAX), [__cdb_rows].[__cdb_array]), N'[]')) AS [__cdb_item] `+
					`WHERE ([__cdb_item].[type] = 1 AND NOT [__cdb_item].[value] IS NULL AND LOWER([__cdb_item].[value]) LIKE @p2 ESCAPE '!') `+
					`GROUP BY [__cdb_item].[value] ORDER BY 2 DESC, 1 ASC OFFSET 0 ROWS FETCH NEXT 20 ROWS ONLY`),
			Entry("clickhouse native string arrays", dialectClickHouse, basePrefix(`"__cdb_base"`)+
				`SELECT "__cdb_item" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total `+
				`FROM (SELECT "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN (@cdb_p1)) AS "__cdb_rows" ARRAY JOIN arrayDistinct("__cdb_rows"."__cdb_array") AS "__cdb_item" `+
				`WHERE (NOT ("__cdb_item" IS NULL) AND LOWER("__cdb_item") LIKE @cdb_p2) `+
				`GROUP BY "__cdb_item" ORDER BY 2 DESC, 1 ASC LIMIT 20`),
		)

		DescribeTable("numbers sibling and search placeholders after authored parameters",
			func(dialect sqlDialect, authoredPlaceholder, siblingPlaceholder, searchPlaceholder string) {
				authored := sqlQuery{Text: "SELECT id, env, tables FROM orders WHERE id = " + sqlParamMarker(0), Args: []any{int64(7)}}
				statement, args, err := lookupSQL(dialect, authored, tablesBinding,
					[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "Pol")
				Expect(err).ToNot(HaveOccurred())
				Expect(statement).To(And(
					ContainSubstring("WHERE id = "+authoredPlaceholder),
					ContainSubstring(siblingPlaceholder),
					ContainSubstring(searchPlaceholder),
				))
				Expect(args).To(Equal([]any{int64(7), "prod", "%pol%"}))
			},
			Entry("postgres", dialectPostgres, "$1", `WHERE "env" IN ($2)`, `LIKE $3 ESCAPE '!'`),
			Entry("sqlserver", dialectSQLServer, "@p1", `WHERE [env] IN (@p2)`, `LIKE @p3 ESCAPE '!'`),
		)
	})

	// A range and a toggle are typed, not picked, so there is no list to offer.
	It("refuses a lookup on a filter with no values to list", func() {
		_, _, err := lookupSQL(dialectPostgres, orders, query.ColumnFilterBinding{
			Key: "filter.latency_ms", Field: "latency_ms", Kind: query.ColumnFilterKindRange,
		}, nil, "")
		Expect(err).To(MatchError(ContainSubstring("has no values to list")))
	})
})
