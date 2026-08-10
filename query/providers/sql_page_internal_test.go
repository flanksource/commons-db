package providers

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ReadSQLPage", func() {
	It("executes bounded offset pages and reports the whole-result total", func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.Exec(`
			CREATE TABLE events (id INTEGER PRIMARY KEY, message TEXT);
			INSERT INTO events (id, message) VALUES (1, 'one'), (2, 'two'), (3, 'three');
		`)
		Expect(err).ToNot(HaveOccurred())

		diagnostics := query.NewProviderDiagnostics("clickhouse", "SELECT id, message FROM events", nil)
		first, err := ReadSQLPage(context.Background(), database, models.ConnectionTypeClickHouse, SQLPageRequest{
			Query: "SELECT id, message FROM events ORDER BY id",
			Page:  query.PageRequest{Limit: 2}, Diagnostics: diagnostics,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(first.Rows).To(HaveLen(2))
		Expect(first.HasMore).To(BeTrue())
		Expect(first.Total).To(Equal(&query.Total{Value: 3, Exact: true}))
		snapshot := diagnostics.Snapshot()
		Expect(snapshot.Request.Query).To(HaveSuffix("LIMIT 3"))
		Expect(snapshot.Response.Preview).To(ContainSubstring(`"message": "one"`))

		second, err := ReadSQLPage(context.Background(), database, models.ConnectionTypeClickHouse, SQLPageRequest{
			Query: "SELECT id, message FROM events ORDER BY id",
			Page:  query.PageRequest{Limit: 2, Offset: 2},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(second.Rows).To(ConsistOf(query.Row{"id": int64(3), "message": "three"}))
		Expect(second.HasMore).To(BeFalse())
		Expect(second.Total).To(Equal(&query.Total{Value: 3, Exact: true}))
	})
})
