package sqlite

import (
	"database/sql"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// hostileValue is a filter value written to end the statement it is bound
// into, were it ever written into the statement rather than bound.
const hostileValue = `eu') OR 1=1; DROP TABLE orders; --`

var _ = Describe("SQL statements built as sqlglot trees", func() {
	request := SQLPageRequest{
		Query:    "WITH recent AS (SELECT * FROM orders) SELECT id, region FROM recent",
		Filters:  []query.ColumnFilterValue{terms("region", []string{hostileValue}, nil)},
		Order:    query.Order{{Column: "id", Unique: true}},
		Position: query.CursorPosition{Keys: []any{int64(7)}},
		Page:     query.PageRequest{Limit: 10, Strategy: query.PagingCursor},
	}

	// The statement a driver runs is the generator's output for the tree and
	// nothing else: no text is spliced around it, so a value can only ever be a
	// bind node and an identifier only ever an Identifier node.
	DescribeTable("hands the driver exactly what the generator writes for the tree",
		func(dialect sqlDialect) {
			built, err := buildPagedSQL(dialect, request)
			Expect(err).ToNot(HaveOccurred())
			statement, args, err := built.render()
			Expect(err).ToNot(HaveOccurred())
			glot, err := dialect.glot()
			Expect(err).ToNot(HaveOccurred())
			Expect(sqlglot.Generate(built.tree, glot)).To(Equal(statement))
			Expect(statement).ToNot(ContainSubstring("1=1"))
			Expect(statement).ToNot(ContainSubstring("DROP"))
			Expect(args).To(Equal([]any{hostileValue, int64(7)}))

			reparsed, err := sqlglot.ParseOne(statement, glot)
			Expect(err).ToNot(HaveOccurred())
			Expect(reparsed.FindAll("Placeholder", "Parameter")).To(HaveLen(len(args)))
			Expect(reparsed.FindAll("CTE")).To(HaveLen(2))
		},
		Entry("postgres", dialectPostgres),
		Entry("sqlserver", dialectSQLServer),
		Entry("mysql", dialectMySQL),
		Entry("sqlite", dialectSQLite),
		Entry("clickhouse", dialectClickHouse),
	)

	It("binds a hostile value through a real driver as a value", func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.Exec(`CREATE TABLE orders (id INTEGER, region TEXT);
			INSERT INTO orders VALUES (1, 'eu'), (2, '` + "eu'') OR 1=1; DROP TABLE orders; --" + `')`)
		Expect(err).ToNot(HaveOccurred())

		built, err := buildPagedSQL(dialectSQLite, SQLPageRequest{
			Query:   "SELECT id, region FROM orders",
			Filters: []query.ColumnFilterValue{terms("region", []string{hostileValue}, nil)},
			Page:    query.PageRequest{Limit: 10, SkipTotal: true},
		})
		Expect(err).ToNot(HaveOccurred())
		statement, args, err := built.render()
		Expect(err).ToNot(HaveOccurred())
		var id int
		var region string
		Expect(database.QueryRow(statement, args...).Scan(&id, &region)).To(Succeed())
		Expect(id).To(Equal(2))
		Expect(region).To(Equal(hostileValue))
	})

	// A LIKE … ESCAPE is a condition; T-SQL, which has no boolean type, must not
	// be handed it compared with zero.
	It("writes a T-SQL LIKE with an escape as a condition", func() {
		statement, _, err := filteredSQL(dialectSQLServer, ordersQuery, []query.ColumnFilterValue{
			{Field: "region", Kind: query.ColumnFilterKindText, Include: []string{"eu"}},
			terms("env", []string{"prod"}, nil),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`WHERE (LOWER([region]) LIKE @p1 ESCAPE '!' AND [env] IN (@p2))`))
	})

	// The generator may write a clause in another order than it was read, so a
	// positional dialect's arguments follow the order the generator wrote their
	// markers in, not the order they were bound in.
	It("orders positional arguments as the generator wrote their markers", func() {
		built, err := buildPagedSQL(dialectMySQL, SQLPageRequest{
			Query:     "SELECT id, region FROM orders WHERE env = " + sqlParamMarker(0) + " LIMIT " + sqlParamMarker(1),
			QueryArgs: []any{"prod", 5},
			Filters:   []query.ColumnFilterValue{terms("region", []string{"eu"}, nil)},
			Page:      query.PageRequest{Limit: 10, SkipTotal: true},
		})
		Expect(err).ToNot(HaveOccurred())
		statement, args, err := built.render()
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring("WHERE env = ? LIMIT ?"))
		Expect(args).To(Equal([]any{"prod", 5, "eu"}))
	})

	It("fails naming the dialect when the query cannot be read in it", func() {
		_, err := buildPagedSQL(dialectSQLite, SQLPageRequest{
			Query: "SELECT id FROM orders WHERE", Page: query.PageRequest{Limit: 10},
		})
		Expect(err).To(MatchError(ContainSubstring("sqlite")))
	})

	// A driver numbers placeholders across the whole statement, so the
	// wrapper's values would bind to the author's marker instead of its own.
	DescribeTable("refuses a query that already binds",
		func(dialect sqlDialect, statement string) {
			_, err := buildPagedSQL(dialect, SQLPageRequest{Query: statement, Page: query.PageRequest{Limit: 10}})
			Expect(err).To(MatchError(ContainSubstring("already contains a bind placeholder")))
		},
		Entry("postgres $1", dialectPostgres, "SELECT * FROM t WHERE id = $1"),
		Entry("mysql ?", dialectMySQL, "SELECT * FROM t WHERE id = ?"),
		Entry("sqlserver @p1", dialectSQLServer, "SELECT * FROM t WHERE id = @p1"),
		Entry("sqlite ?", dialectSQLite, "SELECT * FROM t WHERE id = ?"),
		Entry("clickhouse ?", dialectClickHouse, "SELECT * FROM t WHERE id = ?"),
	)

	// Postgres reads "?" as a jsonb operator; only a bind node is a marker.
	DescribeTable("accepts a query whose question mark is not a bind marker",
		func(dialect sqlDialect, statement string) {
			_, err := buildPagedSQL(dialect, SQLPageRequest{Query: statement, Page: query.PageRequest{Limit: 10}})
			Expect(err).ToNot(HaveOccurred())
		},
		Entry("a jsonb key test", dialectPostgres, `SELECT * FROM t WHERE payload ? 'k'`),
		Entry("a question mark in a literal", dialectMySQL, `SELECT 'huh?' AS note`),
		Entry("a dollar-quoted body", dialectPostgres, `SELECT $$a $1 b$$ AS note`),
	)

	// clickhouse-go binds client-side, and its positional "?" cannot be
	// written by the ClickHouse generator, so ClickHouse binds by name.
	It("binds ClickHouse values by the names its markers carry", func() {
		built, err := buildPagedSQL(dialectClickHouse, SQLPageRequest{
			Query:   ordersQuery,
			Filters: []query.ColumnFilterValue{terms("region", []string{"eu", "us"}, nil)},
			Page:    query.PageRequest{Limit: 10, SkipTotal: true},
		})
		Expect(err).ToNot(HaveOccurred())
		statement, args, err := built.render()
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(ContainSubstring(`"region" IN (@cdb_p1, @cdb_p2)`))
		Expect(driverArgs(dialectClickHouse, args)).To(Equal([]any{sql.Named("cdb_p1", "eu"), sql.Named("cdb_p2", "us")}))
		Expect(driverArgs(dialectPostgres, args)).To(Equal(args))
	})

	It("refuses a ClickHouse query that spells one of its bind names", func() {
		_, err := buildPagedSQL(dialectClickHouse, SQLPageRequest{
			Query: "SELECT 'x@cdb_p1' AS note", Page: query.PageRequest{Limit: 10},
		})
		Expect(err).To(MatchError(ContainSubstring("@cdb_p")))
	})
})
