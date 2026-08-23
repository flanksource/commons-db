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

		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "clickhouse", Query: "SELECT id, message FROM events", Detail: query.DiagnosticFull})
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

	It("carries the statement and arguments that failed on the error", func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, message TEXT)`)
		Expect(err).ToNot(HaveOccurred())

		_, err = ReadSQLPage(context.Background(), database, models.ConnectionTypeClickHouse, SQLPageRequest{
			Query:   "SELECT id, premum FROM events ORDER BY id",
			Filters: []query.ColumnFilterValue{{Column: "message", Field: "message", Include: []string{"one"}}},
			Page:    query.PageRequest{Limit: 2},
		})
		Expect(err).To(MatchError(ContainSubstring("premum")))

		diagnostics := query.DiagnosticsFromError(err)
		Expect(diagnostics).ToNot(BeNil())
		Expect(diagnostics.Provider).To(Equal("clickhouse"))
		Expect(diagnostics.Request.Query).To(ContainSubstring("premum"))
		Expect(diagnostics.Request.Arguments).To(ConsistOf("one"))
		Expect(diagnostics.Error).To(ContainSubstring("premum"))
	})

	// A debug run hands its own diagnostics back to the caller, so the error must
	// not sprout a second, shorter copy that a reader could mistake for it.
	It("leaves a debug run's own diagnostics as the only ones", func() {
		database, err := sql.Open("sqlite", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(database.Close)
		_, err = database.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY)`)
		Expect(err).ToNot(HaveOccurred())

		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{Provider: "clickhouse", Query: "SELECT premum FROM events", Detail: query.DiagnosticFull})
		_, err = ReadSQLPage(context.Background(), database, models.ConnectionTypeClickHouse, SQLPageRequest{
			Query: "SELECT premum FROM events", Page: query.PageRequest{Limit: 2}, Diagnostics: diagnostics,
		})
		Expect(err).To(HaveOccurred())
		Expect(query.DiagnosticsFromError(err)).To(BeNil())
		Expect(diagnostics.Snapshot().Error).To(ContainSubstring("premum"))
	})
})
