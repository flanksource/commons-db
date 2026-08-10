package providers

import (
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const ordersQuery = "SELECT id, region, env, latency_ms FROM orders"

// timeZero types the assertion that a date-math bound resolved to an instant.
var timeZero = time.Time{}

func terms(field string, include, exclude []string) query.ColumnFilterValue {
	return query.ColumnFilterValue{
		Field: field, Kind: query.ColumnFilterKindTerms, Include: include, Exclude: exclude,
	}
}

var _ = Describe("buildFilteredSQL", func() {
	// An unfiltered profile must run the statement it has always run, or turning
	// filtering on would change what every existing profile returns.
	It("returns the statement untouched when nothing is selected", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(ordersQuery))
		Expect(args).To(BeNil())
	})

	It("treats an empty selection as no selection", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
			[]query.ColumnFilterValue{terms("region", nil, nil)})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(ordersQuery))
		Expect(args).To(BeNil())
	})

	// The values one field collects are alternatives. One equality per value
	// would AND them and match nothing — the failure a multi-select hits on its
	// second click.
	It("ORs several included values for one field into a single IN", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
			[]query.ColumnFilterValue{terms("region", []string{"us-east", "us-west"}, nil)})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal("WITH \"__cdb_base\" AS (\n" + ordersQuery + "\n)\n" +
			`SELECT * FROM "__cdb_base" WHERE "region" IN ($1,$2)`))
		Expect(args).To(Equal([]any{"us-east", "us-west"}))
	})

	// A row with no value was not one of the excluded values, so excluding must
	// keep it — the same set OpenSearch's must_not:terms leaves behind.
	It("keeps rows with no value when excluding", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
			[]query.ColumnFilterValue{terms("env", nil, []string{"dev"})})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`("env" IS NULL OR "env" NOT IN ($1))`))
		Expect(args).To(Equal([]any{"dev"}))
	})

	It("ANDs across distinct fields while OR-ing within each", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery, []query.ColumnFilterValue{
			terms("region", []string{"us-east", "us-west"}, nil),
			terms("env", []string{"prod"}, nil),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`"region" IN ($1,$2) AND "env" IN ($3)`))
		Expect(args).To(Equal([]any{"us-east", "us-west", "prod"}))
	})

	It("merges a column filter and a list param bound to the same field", func() {
		statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery, []query.ColumnFilterValue{
			{Column: "region", Key: "filter.region", Field: "region",
				Kind: query.ColumnFilterKindTerms, Include: []string{"us-east"}},
			{Key: "regions", Field: "region",
				Kind: query.ColumnFilterKindTerms, Include: []string{"us-west"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`WHERE "region" IN ($1,$2)`))
		Expect(statement).ToNot(ContainSubstring("AND"))
		Expect(args).To(Equal([]any{"us-east", "us-west"}))
	})

	DescribeTable("quotes and binds for each dialect",
		func(dialect sqlDialect, expected string) {
			statement, _, err := buildFilteredSQL(dialect, ordersQuery,
				[]query.ColumnFilterValue{terms("region", []string{"eu"}, nil)})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(expected))
		},
		Entry("postgres", dialectPostgres, `"region" IN ($1)`),
		Entry("mysql", dialectMySQL, "`region` IN (?)"),
		Entry("sqlserver", dialectSQLServer, `[region] IN (@p1)`),
		Entry("clickhouse", dialectClickHouse, `"region" IN (?)`),
	)

	// The regex is the injection boundary; quoting is the belt to its braces.
	DescribeTable("refuses a backend field that is not a plain column name",
		func(field string) {
			_, _, err := buildFilteredSQL(dialectPostgres, ordersQuery,
				[]query.ColumnFilterValue{terms(field, []string{"x"}, nil)})
			Expect(err).To(MatchError(ContainSubstring("not a plain column name")))
		},
		Entry("a statement terminator", "region; drop table orders"),
		Entry("a qualified name", "a.b"),
		Entry("a leading digit", "1abc"),
		Entry("a backtick", "a`b"),
		Entry("a bracket", "[a]"),
		Entry("nothing", ""),
		Entry("longer than any real column", strings.Repeat("a", 200)),
	)

	Describe("kinds beyond a value selection", func() {
		It("binds a numeric range as numbers", func() {
			statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
				[]query.ColumnFilterValue{{
					Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: &query.FilterRange{
						Min: &query.FilterBound{Value: float64(100), Inclusive: true},
						Max: &query.FilterBound{Value: float64(500)},
					},
				}})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(`("latency_ms" >= $1 AND "latency_ms" < $2)`))
			Expect(args).To(Equal([]any{float64(100), float64(500)}))
		})

		// OpenSearch resolves date math itself; SQL has no such thing, so the
		// operand becomes a real instant here.
		It("resolves date math to a bound time", func() {
			_, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
				[]query.ColumnFilterValue{{
					Field: "created_at", Kind: query.ColumnFilterKindTime,
					Range: &query.FilterRange{Min: &query.FilterBound{Value: "now-1h", Inclusive: true}},
				}})
			Expect(err).ToNot(HaveOccurred())
			Expect(args).To(HaveLen(1))
			Expect(args[0]).To(BeAssignableToTypeOf(timeZero))
		})

		It("binds a yes/no toggle as a real boolean", func() {
			selected := true
			statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
				[]query.ColumnFilterValue{{
					Field: "deleted", Kind: query.ColumnFilterKindBoolean, Bool: &selected,
				}})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(`"deleted" IN ($1)`))
			Expect(args).To(Equal([]any{true}))
		})

		It("compiles a substring selection into a case-insensitive LIKE", func() {
			statement, args, err := buildFilteredSQL(dialectPostgres, ordersQuery,
				[]query.ColumnFilterValue{{
					Field: "message", Kind: query.ColumnFilterKindText, Include: []string{"Timeout"},
				}})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(ContainSubstring(`LOWER("message") LIKE $1 ESCAPE '!'`))
			Expect(args).To(Equal([]any{"%timeout%"}))
		})

		It("refuses a field selected as both values and a range", func() {
			_, _, err := buildFilteredSQL(dialectPostgres, ordersQuery, []query.ColumnFilterValue{
				terms("status", []string{"200"}, nil),
				{Field: "status", Kind: query.ColumnFilterKindRange,
					Range: &query.FilterRange{Min: &query.FilterBound{Value: float64(500), Inclusive: true}}},
			})
			Expect(err).To(MatchError(ContainSubstring("filtered as both")))
		})
	})

	Describe("wrapping a query that already has a WITH clause", func() {
		It("hoists the author's CTEs into one flat list", func() {
			authored := "WITH recent AS (SELECT * FROM orders WHERE created_at > now()) SELECT id, region FROM recent"
			statement, _, err := buildFilteredSQL(dialectPostgres, authored,
				[]query.ColumnFilterValue{terms("region", []string{"eu"}, nil)})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(HavePrefix(
				"WITH recent AS (SELECT * FROM orders WHERE created_at > now()), \"__cdb_base\" AS ("))
			Expect(statement).To(ContainSubstring("SELECT id, region FROM recent"))
			// Nesting would be rejected outright by SQL Server.
			Expect(strings.Count(strings.ToUpper(statement), "WITH ")).To(Equal(1))
		})

		It("preserves RECURSIVE on the hoisted prefix", func() {
			authored := "WITH RECURSIVE tree AS (SELECT 1 AS n) SELECT n AS region FROM tree"
			statement, _, err := buildFilteredSQL(dialectPostgres, authored,
				[]query.ColumnFilterValue{terms("region", []string{"1"}, nil)})
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(HavePrefix("WITH RECURSIVE tree AS (SELECT 1 AS n), "))
		})

		It("refuses a query that already defines the wrapper's own CTE", func() {
			authored := "WITH __cdb_base AS (SELECT 1 AS region) SELECT * FROM __cdb_base"
			_, _, err := buildFilteredSQL(dialectPostgres, authored,
				[]query.ColumnFilterValue{terms("region", []string{"1"}, nil)})
			Expect(err).To(MatchError(ContainSubstring("already defines a CTE named")))
		})
	})
})

// An export takes everything, so the size of the whole result is a nicety it can
// trade for a backend that actually streams. A table cannot make that trade — it
// has to say what it is a page of — so the window function stays for scope=page.
var _ = Describe("buildPagedSQL", func() {
	pageOrder := query.Order{{Column: "id", Unique: true}}

	It("counts the whole result on every row when a total is wanted", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, ordersQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`COUNT(*) OVER () AS "__cdb_total"`))
	})

	It("pushes one row past the requested page into the backend", func() {
		statement, _, err := buildPagedSQL(dialectClickHouse, ordersQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HaveSuffix("LIMIT 51"))
	})

	It("binds a mixed-direction keyset after the whole-result count", func() {
		order := query.Order{
			{Column: "region"},
			{Column: "id", Desc: true, Unique: true},
		}
		statement, args, err := buildPagedSQL(
			dialectPostgres,
			ordersQuery,
			[]query.ColumnFilterValue{terms("env", []string{"prod"}, nil)},
			order,
			query.CursorPosition{Keys: []any{"eu", int64(42)}},
			query.PageRequest{Limit: 25, Strategy: query.PagingCursor},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`COUNT(*) OVER () AS "__cdb_total"`))
		Expect(statement).To(ContainSubstring(`WHERE ("region" > $2 OR ("region" = $3 AND "id" < $4))`))
		Expect(statement).To(HaveSuffix("LIMIT 26"))
		Expect(args).To(Equal([]any{"prod", "eu", "eu", int64(42)}))
	})

	It("rejects a null cursor key instead of changing its ordering semantics", func() {
		_, _, err := buildPagedSQL(
			dialectClickHouse,
			ordersQuery,
			nil,
			pageOrder,
			query.CursorPosition{Keys: []any{nil}},
			query.PageRequest{Limit: 25, Strategy: query.PagingCursor},
		)
		Expect(err).To(MatchError(ContainSubstring("null")))
	})

	// COUNT(*) OVER () with no PARTITION BY is a whole-partition window aggregate:
	// the database buffers the entire filtered result into a tuplestore before it
	// emits the first row. Leaving it in is what makes time-to-first-byte equal
	// full-scan time, and makes the export ceiling bound the client but not the
	// server.
	It("omits the window aggregate when the caller waived the total", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, ordersQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50, SkipTotal: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).ToNot(ContainSubstring("COUNT(*) OVER ()"))
		Expect(statement).ToNot(ContainSubstring("__cdb_total"))
		Expect(statement).To(ContainSubstring(`SELECT "__cdb_base".* FROM "__cdb_base"`))
	})

	// A full export is a sequence of bounded backend reads. Each read asks one
	// past its page, while the provider tracks the whole-walk ceiling.
	It("bounds each backend read during an export", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, ordersQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50, SkipTotal: true, Ceiling: 100})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HaveSuffix("LIMIT 51"))
	})

	It("renders the ceiling in the dialect's own spelling", func() {
		statement, _, err := buildPagedSQL(dialectSQLServer, ordersQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50, SkipTotal: true, Ceiling: 100})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HaveSuffix("OFFSET 0 ROWS FETCH NEXT 51 ROWS ONLY"))
	})

	It("bounds an unordered sample without claiming a stable position", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, ordersQuery, nil, nil, query.CursorPosition{},
			query.PageRequest{Limit: 50, SkipTotal: true, Ceiling: 100})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HaveSuffix("LIMIT 51"))
	})

	It("bounds an unordered SQL Server page with an explicit arbitrary order", func() {
		statement, _, err := buildPagedSQL(dialectSQLServer, ordersQuery, nil, nil, query.CursorPosition{},
			query.PageRequest{Limit: 50, Offset: 100})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(HaveSuffix("ORDER BY (SELECT NULL)\nOFFSET 100 ROWS FETCH NEXT 51 ROWS ONLY"))
	})
})

var _ = Describe("takeRowTotal", func() {
	It("reads ClickHouse UInt64 totals", func() {
		row := query.Row{sqlTotalColumn: uint64(17), "id": "row-1"}
		total, ok, err := takeRowTotal(row)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(total).To(Equal(int64(17)))
		Expect(row).ToNot(HaveKey(sqlTotalColumn))
	})

	It("refuses totals that exceed the public signed count", func() {
		row := query.Row{sqlTotalColumn: uint64(1<<63 + 1)}
		_, ok, err := takeRowTotal(row)
		Expect(ok).To(BeFalse())
		Expect(err).To(MatchError(ContainSubstring("exceeds the supported maximum")))
	})
})
