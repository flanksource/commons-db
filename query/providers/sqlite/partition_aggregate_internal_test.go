package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	ctxpkg "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// aggregateFixture is each day's rows: a service, a byte count that is
// sometimes NULL, and a status. The services are spread so most groups span
// several days and one ("batch") exists on one day only.
var aggregateFixture = map[string][][]any{
	"2026-10-29": {{"api", int64(100), "ok"}, {"api", int64(300), "error"}, {"web", nil, "ok"}, {"web", int64(50), "ok"}},
	"2026-10-30": {{"api", int64(200), "ok"}, {"web", int64(70), "error"}, {"batch", int64(5000), "ok"}},
	"2026-10-31": {{"api", nil, "ok"}, {"api", int64(400), "ok"}, {"web", int64(30), "ok"}, {"web", int64(90), "error"}},
}

const aggregateSchema = `CREATE TABLE events (id INTEGER PRIMARY KEY, service TEXT NOT NULL, bytes INTEGER, status TEXT NOT NULL`

func writeAggregateFile(path string, days []string, withDay bool) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	database, err := sql.Open("sqlite", "file:"+path)
	Expect(err).ToNot(HaveOccurred())
	defer func() { Expect(database.Close()).To(Succeed()) }()
	schema := aggregateSchema + ")"
	if withDay {
		schema = aggregateSchema + ", dt TEXT NOT NULL)"
	}
	_, err = database.Exec(schema)
	Expect(err).ToNot(HaveOccurred())
	for _, day := range days {
		for _, row := range aggregateFixture[day] {
			if withDay {
				_, err = database.Exec(`INSERT INTO events (service, bytes, status, dt) VALUES (?, ?, ?, ?)`, append(row, day)...)
			} else {
				_, err = database.Exec(`INSERT INTO events (service, bytes, status) VALUES (?, ?, ?)`, row...)
			}
			Expect(err).ToNot(HaveOccurred())
		}
	}
}

var _ = Describe("aggregates over partitions", func() {
	var dir string
	var union *sql.DB
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		for _, day := range partitionDays {
			writeAggregateFile(filepath.Join(dir, "dt="+day, "events.sqlite"), []string{day}, false)
		}
		unionPath := filepath.Join(dir, "union.sqlite")
		writeAggregateFile(unionPath, partitionDays, true)
		var err error
		union, err = sql.Open("sqlite", "file:"+unionPath)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(union.Close)
	})

	read := func(statement string, order query.Order, filters []query.ColumnFilterValue) ([]query.Row, error) {
		provider, err := query.GetProvider("sqlite")
		Expect(err).ToNot(HaveOccurred())
		return provider.Execute(ctxpkg.New(), query.ProviderRequest{
			Query: statement, Order: order, Filters: filters,
			Options: map[string]any{"partitions": map[string]any{
				"glob": filepath.Join(dir, "dt=*", "events.sqlite"), "labels": dayLabels,
			}},
		})
	}
	oracle := func(statement string, order query.Order, filters []query.ColumnFilterValue) []query.Row {
		result, err := ReadSQLPage(context.Background(), union, models.ConnectionTypeSQLite, SQLPageRequest{
			Query: statement, Order: order, Filters: filters, Page: query.PageRequest{Limit: 1000, SkipTotal: true},
		})
		Expect(err).ToNot(HaveOccurred())
		return result.Rows
	}
	byService := query.Order{{Column: "service", Unique: true}}

	DescribeTable("returns what one union file returns",
		func(statement string, order query.Order) {
			rows, err := read(statement, order, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(rows).ToNot(BeEmpty())
			Expect(rows).To(Equal(oracle(statement, order, nil)))
		},
		Entry("count per group, groups spanning days",
			"SELECT service, count(*) AS n, count(bytes) AS sized FROM events GROUP BY service", byService),
		Entry("sum, total, min and max with NULLs",
			"SELECT service, sum(bytes) AS s, total(bytes) AS t, min(bytes) AS lo, max(bytes) AS hi FROM events GROUP BY service", byService),
		Entry("avg as a sum over a count",
			"SELECT service, avg(bytes) AS mean FROM events GROUP BY service", byService),
		Entry("an aggregate inside an expression and a HAVING",
			"SELECT service, sum(bytes) * 2 AS doubled FROM events WHERE status = 'ok' GROUP BY service HAVING count(*) > 1", byService),
		Entry("a whole-table aggregate with no GROUP BY",
			"SELECT count(*) AS n, avg(bytes) AS mean, max(bytes) AS hi FROM events", query.Order{{Column: "n", Unique: true}}),
		Entry("grouping by a label and an ordinal",
			"SELECT dt, status, count(*) AS n FROM events GROUP BY 1, status",
			query.Order{{Column: "dt"}, {Column: "status", Unique: true}}),
		Entry("grouping by a projection alias",
			"SELECT upper(service) AS s, count(*) AS n FROM events GROUP BY s", query.Order{{Column: "s", Unique: true}}),
		Entry("count DISTINCT, gathered",
			"SELECT status, count(DISTINCT service) AS services FROM events GROUP BY status", query.Order{{Column: "status", Unique: true}}),
		Entry("a registered percentile, gathered",
			"SELECT service, percentile(bytes, 50) AS p50 FROM events GROUP BY service", byService),
		Entry("DISTINCT, gathered",
			"SELECT DISTINCT service FROM events", byService),
		// Ids are left out: each partition numbers its own rows, the union file
		// numbers them all, so an id names different rows in the two.
		Entry("a window over every partition's rows, gathered",
			"SELECT service, dt, bytes, sum(bytes) OVER (PARTITION BY service ORDER BY dt, bytes) AS running FROM events",
			query.Order{{Column: "service"}, {Column: "dt"}, {Column: "bytes", Unique: true}}),
	)

	It("pages an aggregate by cursor and by offset", func() {
		statement := "SELECT service, count(*) AS n FROM events GROUP BY service"
		provider, err := query.GetProvider("sqlite")
		Expect(err).ToNot(HaveOccurred())
		paging := provider.(query.PagingProvider)
		request := query.ProviderRequest{
			Query: statement, Order: byService,
			Options: map[string]any{"partitions": map[string]any{"glob": filepath.Join(dir, "dt=*", "events.sqlite")}},
		}
		var walked []query.Row
		for page, err := range paging.Pages(ctxpkg.New(), request, query.PageRequest{Limit: 1, Strategy: query.PagingCursor}) {
			Expect(err).ToNot(HaveOccurred())
			walked = append(walked, page.Rows...)
		}
		Expect(walked).To(Equal(oracle(statement, byService, nil)))
		for page, err := range paging.Pages(ctxpkg.New(), request, query.PageRequest{Limit: 1, Offset: 2}) {
			Expect(err).ToNot(HaveOccurred())
			Expect(page.Rows).To(Equal([]query.Row{{"service": "web", "n": int64(5)}}))
			break
		}
	})

	It("applies request filters to the combined groups", func() {
		statement := "SELECT dt, count(*) AS n FROM events GROUP BY dt"
		order := query.Order{{Column: "dt", Unique: true}}
		filters := []query.ColumnFilterValue{dayFilter(nil, []string{"2026-10-30"})}
		rows, err := read(statement, order, filters)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(Equal(oracle(statement, order, filters)))
		Expect(rows).To(HaveLen(2))
	})
})
