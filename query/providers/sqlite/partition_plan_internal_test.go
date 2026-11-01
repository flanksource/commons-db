package sqlite

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("planPartitioned", func() {
	var aggregates map[string]bool
	BeforeEach(func() {
		var err error
		aggregates, err = sqliteAggregates()
		Expect(err).ToNot(HaveOccurred())
	})
	plan := func(statement string) (partitionPlan, error) {
		return planPartitioned(sqlQuery{Text: statement}, map[string]string{"dt": "2026-10-30"}, aggregates)
	}

	It("lists the aggregates SQLite registers at runtime as well as its own", func() {
		Expect(aggregates).To(HaveKey("total"))
		Expect(aggregates).To(HaveKey("percentile"))
		Expect(aggregates).ToNot(HaveKey("lower"))
	})

	DescribeTable("classifies a statement by how it combines across partitions",
		func(statement string, expected partitionShape) {
			planned, err := plan(statement)
			Expect(err).ToNot(HaveOccurred())
			Expect(planned.Shape).To(Equal(expected))
		},
		Entry("plain rows", "SELECT id, lower(service) AS service FROM events WHERE id > 2", partitionRows),
		Entry("a two-argument scalar max", "SELECT id, max(id, 3) AS floor FROM events", partitionRows),
		Entry("a union of row reads", "SELECT id FROM events UNION ALL SELECT id FROM archive", partitionRows),
		Entry("count and sum", "SELECT service, count(*) AS n, sum(id) AS s FROM events GROUP BY service", partitionDecomposable),
		Entry("avg, min, max and total", "SELECT avg(id) AS a, min(id) AS lo, max(id) AS hi, total(id) AS t FROM events", partitionDecomposable),
		Entry("a GROUP BY without aggregates", "SELECT service FROM events GROUP BY service", partitionDecomposable),
		Entry("count DISTINCT", "SELECT count(DISTINCT service) AS n FROM events", partitionHolistic),
		Entry("a median", "SELECT median(id) AS m FROM events", partitionHolistic),
		Entry("a registered percentile", "SELECT percentile(id, 90) AS p FROM events", partitionHolistic),
		Entry("a window", "SELECT id, row_number() OVER (ORDER BY id) AS r FROM events", partitionHolistic),
		Entry("DISTINCT", "SELECT DISTINCT service FROM events", partitionHolistic),
	)

	DescribeTable("refuses what it cannot combine",
		func(statement, message string) {
			_, err := plan(statement)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an aggregate inside a union", "SELECT count(*) AS n FROM events UNION ALL SELECT 1", "inside a UNION"),
		Entry("an aggregate with a subquery", "SELECT count(*) AS n FROM events WHERE id IN (SELECT id FROM archive)", "CTE or subquery"),
		Entry("an aggregate over a CTE", "WITH e AS (SELECT * FROM events) SELECT count(*) AS n FROM e", "CTE or subquery"),
		Entry("an unaliased aggregate", "SELECT service, count(*) FROM events GROUP BY service", "needs an alias"),
		Entry("a bare column neither grouped nor aggregated", "SELECT service, id, count(*) AS n FROM events GROUP BY service", `column "id" is neither grouped nor aggregated`),
		Entry("a holistic aggregate over a join", "SELECT median(e.id) AS m FROM events e JOIN archive a ON a.id = e.id", "one unqualified table"),
		Entry("an ordinal naming no projection", "SELECT service, count(*) AS n FROM events GROUP BY 3", "names no projection"),
	)

	It("splits avg into a partial sum and count, writing labels in as literals", func() {
		planned, err := plan(`SELECT dt, service, avg(id) AS mean FROM events WHERE dt = '2026-10-30' GROUP BY dt, service HAVING count(*) > 1 ORDER BY mean`)
		Expect(err).ToNot(HaveOccurred())
		Expect(planned.Table).To(Equal(partialsTable))
		Expect(planned.Member.Text).To(Equal(
			`SELECT '2026-10-30' AS "__cdb_g0", service AS "__cdb_g1", SUM(id) AS "__cdb_a0_sum", COUNT(id) AS "__cdb_a0_count", COUNT(*) AS "__cdb_a1" ` +
				`FROM events WHERE '2026-10-30' = '2026-10-30' GROUP BY '2026-10-30', service`))
		Expect(planned.Final.Text).To(Equal(
			`SELECT "__cdb_g0" AS "dt", "__cdb_g1" AS "service", (CAST(SUM("__cdb_a0_sum") AS REAL) / SUM("__cdb_a0_count")) AS mean ` +
				`FROM "__cdb_partials" GROUP BY "__cdb_g0", "__cdb_g1" HAVING SUM("__cdb_a1") > 1 ORDER BY mean`))
	})

	It("renumbers the param markers each half keeps, duplicating an avg's", func() {
		source := sqlQuery{
			Text: "SELECT service, avg(id * " + sqlParamMarker(0) + ") AS mean FROM events WHERE id > " + sqlParamMarker(1) +
				" GROUP BY service HAVING count(*) > " + sqlParamMarker(2),
			Args: []any{int64(10), int64(2), int64(1)},
		}
		planned, err := planPartitioned(source, nil, aggregates)
		Expect(err).ToNot(HaveOccurred())
		Expect(planned.Member.Args).To(Equal([]any{int64(10), int64(10), int64(2)}))
		Expect(planned.Member.Text).To(Equal(
			`SELECT service AS "__cdb_g0", SUM(id * ` + sqlParamMarker(0) + `) AS "__cdb_a0_sum", COUNT(id * ` + sqlParamMarker(1) + `) AS "__cdb_a0_count", ` +
				`COUNT(*) AS "__cdb_a1" FROM events WHERE id > ` + sqlParamMarker(2) + ` GROUP BY service`))
		Expect(planned.Final.Args).To(Equal([]any{int64(1)}))
		Expect(planned.Final.Text).To(HaveSuffix(`HAVING SUM("__cdb_a1") > ` + sqlParamMarker(0)))
	})

	It("gathers a holistic aggregate's filtered rows under the query's own table", func() {
		planned, err := plan(`SELECT dt, median(id) AS m FROM events WHERE dt = '2026-10-30' AND id > 1 GROUP BY dt`)
		Expect(err).ToNot(HaveOccurred())
		Expect(planned.Table).To(Equal("events"))
		Expect(planned.Member.Text).To(Equal(`SELECT *, '2026-10-30' AS "dt" FROM events WHERE '2026-10-30' = '2026-10-30' AND id > 1`))
		Expect(planned.Final.Text).To(Equal(`SELECT dt, median(id) AS m FROM events WHERE dt = '2026-10-30' AND id > 1 GROUP BY dt`))
	})
})
