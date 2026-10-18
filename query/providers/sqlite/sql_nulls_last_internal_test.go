package sqlite

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Nulls sort last in both directions on every dialect, and the keyset predicate
// resumes in that same order: a null never sorts before a value, and nothing
// sorts after a null except more nulls broken by the tiebreaker.
//
// The generator writes NULLS LAST only where it is not the dialect's default:
// postgres sorts nulls last ascending and first descending, SQLite the other
// way round, and ClickHouse last in both. MySQL and SQL Server cannot say it,
// so a leading IS NULL term does.
var _ = Describe("nulls-last keyset paging", func() {
	const workItems = "SELECT id, durationMs FROM work_items"
	slowestFirst := query.Order{{Column: "durationMs", Desc: true}, {Column: "id", Unique: true}}

	DescribeTable("renders the order and the resume predicate",
		func(dialect sqlDialect, keys []any, expected string, args []any) {
			statement, bound, err := rendered(buildPagedSQL(dialect, SQLPageRequest{
				Query: workItems, Order: slowestFirst, Position: query.CursorPosition{Keys: keys},
				Page: query.PageRequest{Limit: 2, SkipTotal: true, Strategy: query.PagingCursor},
			}))
			Expect(err).ToNot(HaveOccurred())
			Expect(statement).To(Equal(expected))
			Expect(bound).To(Equal(args))
		},
		Entry("sqlite after a value", dialectSQLite, []any{int64(40), "w3"},
			`WITH "__cdb_base" AS (`+workItems+`) SELECT "__cdb_base".* FROM "__cdb_base" `+
				`WHERE (("durationMs" < ? OR "durationMs" IS NULL) OR ("durationMs" = ? AND ("id" > ? OR "id" IS NULL))) `+
				`ORDER BY "durationMs" DESC, "id" ASC NULLS LAST LIMIT 3`,
			[]any{int64(40), int64(40), "w3"}),
		Entry("sqlite inside the nulls", dialectSQLite, []any{nil, "w4"},
			`WITH "__cdb_base" AS (`+workItems+`) SELECT "__cdb_base".* FROM "__cdb_base" `+
				`WHERE ("durationMs" IS NULL AND ("id" > ? OR "id" IS NULL)) `+
				`ORDER BY "durationMs" DESC, "id" ASC NULLS LAST LIMIT 3`,
			[]any{"w4"}),
		Entry("postgres after a value", dialectPostgres, []any{int64(40), "w3"},
			`WITH "__cdb_base" AS (`+workItems+`) SELECT "__cdb_base".* FROM "__cdb_base" `+
				`WHERE (("durationMs" < $1 OR "durationMs" IS NULL) OR ("durationMs" = $2 AND ("id" > $3 OR "id" IS NULL))) `+
				`ORDER BY "durationMs" DESC NULLS LAST, "id" ASC LIMIT 3`,
			[]any{int64(40), int64(40), "w3"}),
		Entry("postgres inside the nulls", dialectPostgres, []any{nil, "w4"},
			`WITH "__cdb_base" AS (`+workItems+`) SELECT "__cdb_base".* FROM "__cdb_base" `+
				`WHERE ("durationMs" IS NULL AND ("id" > $1 OR "id" IS NULL)) `+
				`ORDER BY "durationMs" DESC NULLS LAST, "id" ASC LIMIT 3`,
			[]any{"w4"}),
		Entry("clickhouse after a value", dialectClickHouse, []any{int64(40), "w3"},
			`WITH "__cdb_base" AS (`+workItems+`) SELECT "__cdb_base".* FROM "__cdb_base" `+
				`WHERE (("durationMs" < @cdb_p1 OR "durationMs" IS NULL) OR ("durationMs" = @cdb_p2 AND ("id" > @cdb_p3 OR "id" IS NULL))) `+
				`ORDER BY "durationMs" DESC, "id" ASC LIMIT 3`,
			[]any{int64(40), int64(40), "w3"}),
		Entry("sqlserver after a value", dialectSQLServer, []any{int64(40), "w3"},
			"WITH [__cdb_base] AS (SELECT id AS id, durationMs AS durationMs FROM work_items) SELECT [__cdb_base].* FROM [__cdb_base] "+
				"WHERE (([durationMs] < @p1 OR [durationMs] IS NULL) OR ([durationMs] = @p2 AND ([id] > @p3 OR [id] IS NULL))) "+
				"ORDER BY CASE WHEN [durationMs] IS NULL THEN 1 ELSE 0 END, [durationMs] DESC, CASE WHEN [id] IS NULL THEN 1 ELSE 0 END, [id] ASC "+
				"OFFSET 0 ROWS FETCH NEXT 3 ROWS ONLY",
			[]any{int64(40), int64(40), "w3"}),
		Entry("sqlserver inside the nulls", dialectSQLServer, []any{nil, "w4"},
			"WITH [__cdb_base] AS (SELECT id AS id, durationMs AS durationMs FROM work_items) SELECT [__cdb_base].* FROM [__cdb_base] "+
				"WHERE ([durationMs] IS NULL AND ([id] > @p1 OR [id] IS NULL)) "+
				"ORDER BY CASE WHEN [durationMs] IS NULL THEN 1 ELSE 0 END, [durationMs] DESC, CASE WHEN [id] IS NULL THEN 1 ELSE 0 END, [id] ASC "+
				"OFFSET 0 ROWS FETCH NEXT 3 ROWS ONLY",
			[]any{"w4"}),
		Entry("mysql inside the nulls", dialectMySQL, []any{nil, "w4"},
			"WITH `__cdb_base` AS ("+workItems+") SELECT `__cdb_base`.* FROM `__cdb_base` "+
				"WHERE (`durationMs` IS NULL AND (`id` > ? OR `id` IS NULL)) "+
				"ORDER BY CASE WHEN `durationMs` IS NULL THEN 1 ELSE 0 END, `durationMs` DESC, CASE WHEN `id` IS NULL THEN 1 ELSE 0 END, `id` ASC "+
				"LIMIT 3",
			[]any{"w4"}),
	)

	It("rejects a null key for the unique tiebreaker column", func() {
		_, err := buildPagedSQL(dialectSQLite, SQLPageRequest{
			Query: workItems, Order: slowestFirst, Position: query.CursorPosition{Keys: []any{int64(40), nil}},
			Page: query.PageRequest{Limit: 2, Strategy: query.PagingCursor},
		})
		Expect(err).To(MatchError(ContainSubstring(`cursor key 1 for unique column "id" is null`)))
	})
})
