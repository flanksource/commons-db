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

	Describe("an array column", func() {
		tablesBinding := query.ColumnFilterBinding{
			Column: "tables", Key: "filter.tables", Field: "tables",
			Kind: query.ColumnFilterKindTerms, Array: true, Lookup: true, Multi: true,
		}

		// Each element is a value, counted once per row that holds it — however
		// many times that row's array repeats it.
		DescribeTable("lists string array members with row counts in every SQL dialect",
			func(dialect sqlDialect, expected string) {
				statement, args, err := buildLookupSQL(dialect, ordersQuery, tablesBinding,
					[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "Pol", 20)
				Expect(err).ToNot(HaveOccurred())
				Expect(statement).To(Equal(expected))
				Expect(args).To(Equal([]any{"prod", "%pol%"}))
			},
			Entry("sqlite JSON", dialectSQLite,
				"WITH \"__cdb_base\" AS (\n"+ordersQuery+"\n)\n"+
					`SELECT "__cdb_item".value AS value, COUNT(DISTINCT "__cdb_rows"."__cdb_row") AS count, COUNT(*) OVER () AS total`+"\n"+
					`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS "__cdb_row", "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN (?)) AS "__cdb_rows" CROSS JOIN json_each("__cdb_rows"."__cdb_array") AS "__cdb_item"`+"\n"+
					`WHERE ("__cdb_item".type = 'text' AND "__cdb_item".value IS NOT NULL AND LOWER("__cdb_item".value) LIKE ? ESCAPE '!')`+"\n"+
					`GROUP BY "__cdb_item".value`+"\n"+
					"ORDER BY 2 DESC, 1 ASC\nLIMIT 20"),
			Entry("postgres native arrays, json, and jsonb", dialectPostgres,
				"WITH \"__cdb_base\" AS (\n"+ordersQuery+"\n)\n"+
					`SELECT ("__cdb_item"."__cdb_value" #>> '{}') AS value, COUNT(DISTINCT "__cdb_rows"."__cdb_row") AS count, COUNT(*) OVER () AS total`+"\n"+
					`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS "__cdb_row", "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN ($1)) AS "__cdb_rows" CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(to_jsonb("__cdb_rows"."__cdb_array"), 'null'::jsonb), '[]'::jsonb)) AS "__cdb_item"("__cdb_value")`+"\n"+
					`WHERE (jsonb_typeof("__cdb_item"."__cdb_value") = 'string' AND ("__cdb_item"."__cdb_value" #>> '{}') IS NOT NULL AND LOWER(("__cdb_item"."__cdb_value" #>> '{}')) LIKE $2 ESCAPE '!')`+"\n"+
					`GROUP BY ("__cdb_item"."__cdb_value" #>> '{}')`+"\n"+
					"ORDER BY 2 DESC, 1 ASC\nLIMIT 20"),
			Entry("mysql JSON", dialectMySQL,
				"WITH `__cdb_base` AS (\n"+ordersQuery+"\n)\n"+
					"SELECT JSON_UNQUOTE(`__cdb_item`.`__cdb_value`) AS value, COUNT(DISTINCT `__cdb_rows`.`__cdb_row`) AS count, COUNT(*) OVER () AS total\n"+
					"FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS `__cdb_row`, `tables` AS `__cdb_array` FROM `__cdb_base` WHERE `env` IN (?)) AS `__cdb_rows` CROSS JOIN JSON_TABLE(IF(JSON_TYPE(`__cdb_rows`.`__cdb_array`) = 'ARRAY', `__cdb_rows`.`__cdb_array`, JSON_ARRAY()), '$[*]' COLUMNS (`__cdb_value` JSON PATH '$')) AS `__cdb_item`\n"+
					"WHERE (JSON_TYPE(`__cdb_item`.`__cdb_value`) = 'STRING' AND JSON_UNQUOTE(`__cdb_item`.`__cdb_value`) IS NOT NULL AND LOWER(JSON_UNQUOTE(`__cdb_item`.`__cdb_value`)) LIKE ? ESCAPE '!')\n"+
					"GROUP BY JSON_UNQUOTE(`__cdb_item`.`__cdb_value`)\nORDER BY 2 DESC, 1 ASC\nLIMIT 20"),
			Entry("sqlserver JSON text and ntext", dialectSQLServer,
				"WITH [__cdb_base] AS (\n"+ordersQuery+"\n)\n"+
					`SELECT [__cdb_item].[value] AS value, COUNT(DISTINCT [__cdb_rows].[__cdb_row]) AS count, COUNT(*) OVER () AS total`+"\n"+
					`FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS [__cdb_row], [tables] AS [__cdb_array] FROM [__cdb_base] WHERE [env] IN (@p1)) AS [__cdb_rows] CROSS APPLY OPENJSON(COALESCE(CONVERT(nvarchar(max), [__cdb_rows].[__cdb_array]), N'[]')) AS [__cdb_item]`+"\n"+
					`WHERE ([__cdb_item].[type] = 1 AND [__cdb_item].[value] IS NOT NULL AND LOWER([__cdb_item].[value]) LIKE @p2 ESCAPE '!')`+"\n"+
					`GROUP BY [__cdb_item].[value]`+"\n"+
					"ORDER BY 2 DESC, 1 ASC\nOFFSET 0 ROWS FETCH NEXT 20 ROWS ONLY"),
			Entry("clickhouse native string arrays", dialectClickHouse,
				"WITH \"__cdb_base\" AS (\n"+ordersQuery+"\n)\n"+
					`SELECT "__cdb_item" AS value, COUNT(*) AS count, COUNT(*) OVER () AS total`+"\n"+
					`FROM (SELECT "tables" AS "__cdb_array" FROM "__cdb_base" WHERE "env" IN (?)) AS "__cdb_rows" ARRAY JOIN arrayDistinct("__cdb_rows"."__cdb_array") AS "__cdb_item"`+"\n"+
					`WHERE ("__cdb_item" IS NOT NULL AND lower("__cdb_item") LIKE ?)`+"\n"+
					`GROUP BY "__cdb_item"`+"\n"+
					"ORDER BY 2 DESC, 1 ASC\nLIMIT 20"),
		)

		DescribeTable("offsets sibling and search placeholders after authored parameters",
			func(dialect sqlDialect, authoredPlaceholder, siblingPlaceholder, searchPlaceholder string) {
				authored := "SELECT id, env, tables FROM orders WHERE id = " + sqlParamMarker(0)
				statement, args, err := buildLookupSQL(dialect, authored, tablesBinding,
					[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)}, "Pol", 20)
				Expect(err).ToNot(HaveOccurred())
				statement, err = materializeSQLParams(dialect, statement, []any{int64(7)}, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(statement).To(And(
					ContainSubstring("WHERE id = "+authoredPlaceholder),
					ContainSubstring(siblingPlaceholder),
					ContainSubstring(searchPlaceholder),
				))
				Expect(args).To(Equal([]any{"prod", "%pol%"}))
			},
			Entry("postgres", dialectPostgres, "$1", `WHERE "env" IN ($2)`, `LIKE $3 ESCAPE '!'`),
			Entry("sqlserver", dialectSQLServer, "@p1", `WHERE [env] IN (@p2)`, `LIKE @p3 ESCAPE '!'`),
		)
	})

	// A range and a toggle are typed, not picked, so there is no list to offer.
	It("refuses a lookup on a filter with no values to list", func() {
		_, _, err := buildLookupSQL(dialectPostgres, ordersQuery, query.ColumnFilterBinding{
			Key: "filter.latency_ms", Field: "latency_ms", Kind: query.ColumnFilterKindRange,
		}, nil, "", 20)
		Expect(err).To(MatchError(ContainSubstring("has no values to list")))
	})
})
