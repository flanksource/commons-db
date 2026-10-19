package sqlite

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL profile parameters", func() {
	provider := sqlProvider{key: "postgres"}

	It("separates direct scalar and list references from the statement", func() {
		parameterized, err := provider.ParameterizeQuery(query.QueryParameterizationRequest{
			Query: `SELECT {{.params.value}} AS value WHERE tenant = {{ index .params "tenant-id" }} AND region IN ({{.params.regions}})`,
			Params: map[string]any{
				"value": "x' OR TRUE --", "tenant-id": "tenant-x", "regions": []string{"eu", "us-east"},
			},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(parameterized.Query).To(Equal(
			"SELECT __cdb_query_param_0__ AS value WHERE tenant = __cdb_query_param_1__ " +
				"AND region IN (__cdb_query_param_2__, __cdb_query_param_3__)",
		))
		Expect(parameterized.Args).To(Equal([]any{"x' OR TRUE --", "tenant-x", "eu", "us-east"}))
		Expect(parameterized.UsedParams).To(Equal([]string{"regions", "tenant-id", "value"}))
	})

	It("separates identifier references from driver-bound values", func() {
		parameterized, err := provider.ParameterizeQuery(query.QueryParameterizationRequest{
			Query: `SELECT {{.params.column}} FROM {{.params.table}} WHERE env = {{.params.environment}}`,
			Params: map[string]any{
				"column": "region", "table": "analytics.orders", "environment": "prod' OR TRUE --",
			},
			Definitions: []query.ParamDef{
				{Name: "column", Type: query.ParamTypeIdentifier},
				{Name: "table", Type: query.ParamTypeIdentifier},
				{Name: "environment", Type: query.ParamTypeString},
			},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(parameterized.Query).To(Equal(
			"SELECT " + sqlIdentifierMarker(0) + " FROM " + sqlIdentifierMarker(1) +
				" WHERE env = " + sqlParamMarker(0),
		))
		Expect(parameterized.Identifiers).To(Equal([]string{"region", "analytics.orders"}))
		Expect(parameterized.Args).To(Equal([]any{"prod' OR TRUE --"}))
		Expect(parameterized.UsedParams).To(Equal([]string{"column", "environment", "table"}))

		statement, args, err := rendered(buildColumnStatsSQL(dialectPostgres, sqlQuery{
			Text: parameterized.Query, Args: parameterized.Args, Identifiers: parameterized.Identifiers,
		}, []string{"region"}))
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HavePrefix(
			`WITH "__cdb_base" AS (SELECT "region" FROM "analytics"."orders" WHERE env = $1)`,
		))
		Expect(args).To(Equal([]any{"prod' OR TRUE --"}))
	})

	It("rejects an unsafe identifier before opening a connection", func() {
		_, err := provider.ParameterizeQuery(query.QueryParameterizationRequest{
			Query:       `SELECT * FROM {{.params.table}}`,
			Params:      map[string]any{"table": "orders; DROP TABLE users"},
			Definitions: []query.ParamDef{{Name: "table", Type: query.ParamTypeIdentifier}},
		})
		Expect(err).To(MatchError(ContainSubstring("is not a valid SQL identifier")))
	})

	DescribeTable("rejects a reference that cannot be bound as a value",
		func(statement, message string) {
			_, err := provider.ParameterizeQuery(query.QueryParameterizationRequest{
				Query: statement, Params: map[string]any{"value": "x", "ids": []string{"a"}},
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("inside a string literal", `SELECT '{{.params.value}}'`, "must not be quoted"),
		Entry("inside a comment", `SELECT 1 -- {{.params.value}}`, "must not be quoted or commented"),
		Entry("through a template function", `SELECT {{ join .params.ids "," }}`, "only direct, unquoted"),
	)

	// stats is the column-stats statement over base, the smallest wrapper that
	// materializes the author's params.
	stats := func(dialect sqlDialect, base sqlQuery) (string, []any, error) {
		return rendered(buildColumnStatsSQL(dialect, base, []string{"id"}))
	}

	DescribeTable("materializes the driver's placeholder spelling",
		func(dialect sqlDialect, expected string) {
			statement, args, err := stats(dialect, sqlQuery{Text: "SELECT " + sqlParamMarker(0) + " AS id", Args: []any{"value"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring("(SELECT " + expected + " AS id)"))
			Expect(args).To(Equal([]any{"value"}))
		},
		Entry("postgres", dialectPostgres, "$1"),
		Entry("sqlserver", dialectSQLServer, "@p1"),
		Entry("mysql", dialectMySQL, "?"),
		Entry("sqlite", dialectSQLite, "?"),
		Entry("clickhouse", dialectClickHouse, "@cdb_p1"),
	)

	DescribeTable("quotes identifier paths for the connected dialect",
		func(dialect sqlDialect, identifier, expected string) {
			statement, _, err := stats(dialect, sqlQuery{
				Text: "SELECT id FROM " + sqlIdentifierMarker(0), Identifiers: []string{identifier},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(" FROM " + expected + ")"))
		},
		Entry("postgres schema and table", dialectPostgres, "analytics.orders", `"analytics"."orders"`),
		Entry("sqlserver database, schema, and table", dialectSQLServer, "warehouse.analytics.orders", `[warehouse].[analytics].[orders]`),
		Entry("mysql database and table", dialectMySQL, "analytics.orders", "`analytics`.`orders`"),
		Entry("clickhouse database and table", dialectClickHouse, "analytics.orders", `"analytics"."orders"`),
	)

	It("qualifies a column by an identifier path", func() {
		statement, _, err := stats(dialectPostgres, sqlQuery{
			Text:        "SELECT " + sqlIdentifierMarker(0) + ".id FROM orders",
			Identifiers: []string{"analytics.orders"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`(SELECT "analytics"."orders".id FROM orders)`))
	})

	It("refuses an identifier path where only a single name can stand", func() {
		_, _, err := stats(dialectPostgres, sqlQuery{
			Text:        "SELECT id AS " + sqlIdentifierMarker(0) + " FROM orders",
			Identifiers: []string{"analytics.orders"},
		})
		Expect(err).To(MatchError(ContainSubstring("dotted path")))
	})

	DescribeTable("rejects unsafe identifier paths",
		func(identifier string) {
			_, err := validateSQLIdentifierPath(identifier)
			Expect(err).To(MatchError(ContainSubstring("is not a valid SQL identifier")))
		},
		Entry("statement separator", "orders; DROP TABLE users"),
		Entry("empty segment", "analytics..orders"),
		Entry("wildcard", "analytics.*"),
	)

	It("numbers filter and cursor values after the profile arguments", func() {
		statement, args, err := rendered(buildPagedSQL(dialectPostgres, SQLPageRequest{
			Query:     "SELECT id, region, env FROM orders WHERE env = " + sqlParamMarker(0),
			QueryArgs: []any{"prod"},
			Filters:   []query.ColumnFilterValue{terms("region", []string{"eu"}, nil)},
			Order:     query.Order{{Column: "id", Unique: true}},
			Position:  query.CursorPosition{Keys: []any{int64(10)}},
			Page:      query.PageRequest{Limit: 20, Strategy: query.PagingCursor},
		}))
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring("env = $1"))
		Expect(statement).To(ContainSubstring(`"region" IN ($2)`))
		Expect(statement).To(ContainSubstring(`"id" > $3`))
		Expect(args).To(Equal([]any{"prod", "eu", int64(10)}))
	})

	It("refuses a param marker that survived inside a literal", func() {
		_, _, err := stats(dialectPostgres, sqlQuery{
			Text: "SELECT '" + sqlParamMarker(0) + "' AS id", Args: []any{"value"},
		})
		Expect(err).To(MatchError(ContainSubstring("is missing")))
	})
})
