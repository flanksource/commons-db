package providers_test

import (
	"fmt"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The SQL column filter is exercised against a real engine, because the whole
// point of wrapping the author's statement in a CTE is what the server then
// makes of it. Only postgres is covered — dbtest embeds postgres and nothing
// else, so MySQL, SQL Server and ClickHouse are covered by the generated-SQL
// assertions in sql_filter_internal_test.go and not by execution.
var _ = Describe("sql column filters (postgres)", Ordered, func() {
	var dsn string

	BeforeAll(func() {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "sql_column_filters"})
		dsn = db.DSN()

		_, err := db.SQL().Exec(`
			DROP TABLE IF EXISTS orders;
			CREATE TABLE orders (
				id         int primary key,
				region     text,
				env        text,
				latency_ms int,
				created_at timestamptz,
				payload    jsonb
			);
			INSERT INTO orders VALUES
				(1, 'us-east', 'prod',    100, now() - interval '2 hours',   '{"k": 1}'),
				(2, 'us-west', 'prod',    250, now() - interval '30 minutes','{"k": 2}'),
				(3, 'eu',      'dev',     500, now() - interval '10 minutes','{}'),
				(4, 'us-east', NULL,       50, now() - interval '5 minutes', '{"k": 3}'),
				(5, '50%',     'prod',     75, now() - interval '1 minute',  '{}'),
				(6, 'US-East', 'staging', 900, now() - interval '3 hours',   '{}');
		`)
		Expect(err).ToNot(HaveOccurred())
	})

	profileFor := func(statement string) query.Profile {
		return query.Profile{
			Name:     "orders",
			Provider: query.ProviderConfig{Type: "sql", Options: map[string]any{"type": "postgres", "url": dsn}},
			Query:    statement,
			Columns: []query.ColumnDef{
				{Name: "id", Type: query.ColumnTypeNumber},
				{Name: "region", Type: query.ColumnTypeString},
				{Name: "env", Type: query.ColumnTypeString},
				{Name: "latency_ms", Type: query.ColumnTypeNumber},
				{Name: "created_at", Type: query.ColumnTypeDateTime},
			},
		}
	}

	ids := func(rows []query.Row) []int {
		out := make([]int, 0, len(rows))
		for _, row := range rows {
			out = append(out, int(row["id"].(int64)))
		}
		return out
	}

	run := func(statement string, params map[string]any) []query.Row {
		GinkgoHelper()
		result, err := query.Execute(context.New(), profileFor(statement), params)
		Expect(err).ToNot(HaveOccurred())
		return result.Rows
	}

	const selectOrders = "SELECT id, region, env, latency_ms, created_at FROM orders ORDER BY id"

	It("advertises a filter for every scalar column of a SQL profile", func() {
		bindings, err := profileFor(selectOrders).ColumnFilterBindings()
		Expect(err).ToNot(HaveOccurred())
		kinds := map[string]query.ColumnFilterKind{}
		for _, binding := range bindings {
			kinds[binding.Column] = binding.Kind
		}
		Expect(kinds).To(Equal(map[string]query.ColumnFilterKind{
			"id":         query.ColumnFilterKindRange,
			"region":     query.ColumnFilterKindTerms,
			"env":        query.ColumnFilterKindTerms,
			"latency_ms": query.ColumnFilterKindRange,
			"created_at": query.ColumnFilterKindTime,
		}))
	})

	It("returns the unfiltered result when nothing is selected", func() {
		Expect(ids(run(selectOrders, nil))).To(Equal([]int{1, 2, 3, 4, 5, 6}))
	})

	It("keeps only the included values", func() {
		Expect(ids(run(selectOrders, map[string]any{"filter.region": "us-east"}))).To(Equal([]int{1, 4}))
	})

	It("ORs the values one field collects", func() {
		Expect(ids(run(selectOrders, map[string]any{"filter.region": "us-east,eu"}))).
			To(Equal([]int{1, 3, 4}))
	})

	// A row with no value was not one of the excluded values. OpenSearch's
	// must_not:terms keeps it, and the two backends must not disagree about what
	// excluding a value means.
	It("keeps a row with no value when excluding", func() {
		Expect(ids(run(selectOrders, map[string]any{"filter.env": "!dev"}))).
			To(Equal([]int{1, 2, 4, 5, 6}))
	})

	It("ANDs across distinct fields", func() {
		Expect(ids(run(selectOrders, map[string]any{
			"filter.region": "us-east,us-west", "filter.env": "prod",
		}))).To(Equal([]int{1, 2}))
	})

	It("bounds a numeric column", func() {
		Expect(ids(run(selectOrders, map[string]any{"filter.latency_ms": ">=100,<500"}))).
			To(Equal([]int{1, 2}))
	})

	It("bounds a time column with date math", func() {
		Expect(ids(run(selectOrders, map[string]any{"filter.created_at": ">=now-1h"}))).
			To(Equal([]int{2, 3, 4, 5}))
	})

	It("merges a column filter and a list param bound to the same field", func() {
		profile := profileFor(selectOrders)
		profile.Params = []query.ParamDef{{Name: "regions", Type: query.ParamTypeList, Field: "region"}}
		result, err := query.Execute(context.New(), profile, map[string]any{
			"filter.region": "us-east", "regions": "us-west",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(ids(result.Rows)).To(Equal([]int{1, 2, 4}))
	})

	It("filters a query that already defines its own CTE", func() {
		authored := "WITH recent AS (SELECT * FROM orders WHERE latency_ms < 600) " +
			"SELECT id, region, env, latency_ms, created_at FROM recent ORDER BY id"
		Expect(ids(run(authored, map[string]any{"filter.region": "us-east"}))).To(Equal([]int{1, 4}))
	})

	// The single highest-value regression here. squirrel's placeholder rewriter
	// is a whole-string scan with no idea which "?" are inside SQL, so if the
	// author's statement ever passed through it this jsonb operator would be
	// renumbered into a bind marker and the query would silently change meaning.
	It("leaves a jsonb ? operator alone", func() {
		authored := "SELECT id, region, env, latency_ms, created_at FROM orders " +
			"WHERE payload ? 'k' ORDER BY id"
		Expect(ids(run(authored, map[string]any{"filter.region": "us-east"}))).To(Equal([]int{1, 4}))
	})

	// Without this refusal, the filter's own value would bind to the author's
	// marker and a query that fails cleanly today would start returning
	// confidently wrong rows.
	It("refuses to filter a query that already binds a placeholder", func() {
		authored := "SELECT id, region FROM orders WHERE region = $1"
		_, err := query.Execute(context.New(), profileFor(authored),
			map[string]any{"filter.region": "us-east"})
		Expect(err).To(MatchError(ContainSubstring("already contains a bind placeholder")))
	})

	It("surfaces the engine's own error for a field the query does not return", func() {
		profile := profileFor("SELECT id, region FROM orders ORDER BY id")
		profile.Columns = append(profile.Columns, query.ColumnDef{
			Name: "missing", Filter: &query.ColumnFilterDef{Field: "nosuchcolumn"},
		})
		_, err := query.Execute(context.New(), profile, map[string]any{"filter.missing": "x"})
		Expect(err).To(MatchError(ContainSubstring("nosuchcolumn")))
	})

	Describe("value lookup", func() {
		lookup := func(key, search string, params map[string]any) ([]query.FilterOption, int) {
			GinkgoHelper()
			options, total, err := query.LookupFilterValues(
				context.New(), profileFor(selectOrders), params, key, search, 20)
			Expect(err).ToNot(HaveOccurred())
			return options, total
		}

		It("lists distinct values with their counts, most common first", func() {
			options, total := lookup("filter.region", "", nil)
			Expect(options[0]).To(Equal(query.FilterOption{Value: "us-east", Count: 2}))
			Expect(total).To(Equal(5))
		})

		It("offers no NULL, which is not a value anyone can select", func() {
			options, _ := lookup("filter.env", "", nil)
			values := make([]string, 0, len(options))
			for _, option := range options {
				values = append(values, option.Value)
			}
			Expect(values).To(ConsistOf("prod", "dev", "staging"))
		})

		// A selection must not hide its own alternatives, but every other active
		// selection still scopes the question.
		It("scopes the options by the sibling filters and not by its own", func() {
			options, _ := lookup("filter.region", "", map[string]any{
				"filter.env": "prod", "filter.region": "eu",
			})
			values := make([]string, 0, len(options))
			for _, option := range options {
				values = append(values, option.Value)
			}
			Expect(values).To(ConsistOf("us-east", "us-west", "50%"))
		})

		It("matches a search term case-insensitively", func() {
			options, _ := lookup("filter.region", "us", nil)
			Expect(options).To(HaveLen(3))
		})

		// Someone typing "50%" means a literal "50%", not "everything".
		It("treats a wildcard in the search term as a literal", func() {
			options, _ := lookup("filter.region", "50%", nil)
			Expect(options).To(Equal([]query.FilterOption{{Value: "50%", Count: 1}}))
		})

		It("refuses a lookup on a filter that has no values to list", func() {
			_, _, err := query.LookupFilterValues(
				context.New(), profileFor(selectOrders), nil, "filter.latency_ms", "", 20)
			Expect(err).To(MatchError(ContainSubstring("has no values to list")))
		})
	})

	It("pages the filtered result rather than the whole one", func() {
		profile := profileFor(selectOrders)
		profile.Order = query.Order{{Column: "id", Unique: true}}

		var pages [][]int
		for page, err := range query.ExecutePages(context.New(), profile,
			query.PageRequest{Limit: 1}, map[string]any{"filter.region": "us-east"}) {
			Expect(err).ToNot(HaveOccurred())
			pages = append(pages, ids(page.Rows))
			if !page.HasMore {
				break
			}
		}
		Expect(pages).To(Equal([][]int{{1}, {4}}), fmt.Sprintf("pages: %v", pages))
	})
})
