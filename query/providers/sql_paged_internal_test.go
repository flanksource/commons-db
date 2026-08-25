package providers

import (
	"strings"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The generated-SQL assertions in sql_filter_internal_test.go say what the
// statement looks like. These say what the server does with it, which is the
// only thing that decides whether an export streams: COUNT(*) OVER () with no
// PARTITION BY is a whole-partition window aggregate, and Postgres' WindowAgg
// must buffer its entire input into a tuplestore before it emits the first row.
// Asserting the text alone would not notice a planner that materialized anyway.
var _ = Describe("buildPagedSQL against postgres", Ordered, func() {
	var explain func(statement string) string

	pageOrder := query.Order{{Column: "id", Unique: true}}

	BeforeAll(func() {
		database := dbtest.ForGinkgo(dbtest.Options{Name: "sql_paged_plan"})
		_, err := database.SQL().Exec(`
			DROP TABLE IF EXISTS wide;
			CREATE TABLE wide (id int primary key, region text);
			INSERT INTO wide SELECT generate_series(1, 5000), 'r';
			ANALYZE wide;
		`)
		Expect(err).ToNot(HaveOccurred())

		explain = func(statement string) string {
			rows, err := database.SQL().Query("EXPLAIN (ANALYZE, BUFFERS) " + statement)
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = rows.Close() }()
			var plan strings.Builder
			for rows.Next() {
				var line string
				Expect(rows.Scan(&line)).To(Succeed())
				plan.WriteString(line)
				plan.WriteString("\n")
			}
			Expect(rows.Err()).ToNot(HaveOccurred())
			return plan.String()
		}
	})

	const wideQuery = "SELECT id, region FROM wide"

	It("materializes the whole result to state an exact total", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, wideQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50})
		Expect(err).ToNot(HaveOccurred())
		Expect(explain(statement)).To(ContainSubstring("WindowAgg"))
	})

	// The point of the whole change: with the total waived and the ceiling pushed
	// down, the plan has no buffering aggregate and the server stops where the
	// export stops instead of scanning past it into rows nobody receives.
	It("neither buffers nor overruns the ceiling when the total is waived", func() {
		statement, _, err := buildPagedSQL(dialectPostgres, wideQuery, nil, pageOrder, query.CursorPosition{},
			query.PageRequest{Limit: 50, SkipTotal: true, Ceiling: 100})
		Expect(err).ToNot(HaveOccurred())

		plan := explain(statement)
		Expect(plan).ToNot(ContainSubstring("WindowAgg"))
		Expect(plan).To(ContainSubstring("Limit"))
		// Each backend read is one page plus one row to prove continuation. The
		// provider, not one unbounded statement, enforces the whole-walk ceiling.
		Expect(plan).To(MatchRegexp(`Limit .*rows=51`))
	})
})
