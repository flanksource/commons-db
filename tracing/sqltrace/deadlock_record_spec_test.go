package sqltrace

import (
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing/xetrace"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("captured deadlock rows", func() {
	It("stores a decoded graph and its XML in the sql_xevent stream", func() {
		connection, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(connection.Close()).To(Succeed())
			Expect(mock.ExpectationsWereMet()).To(Succeed())
		})
		mock.ExpectQuery("SELECT DB_ID()").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
		mock.ExpectClose()

		env := newEnvironment(nil)
		appender := recordAppender{store: env.store(), ctx: env.ctx(), stream: "deadlock-capture", db: connection}
		report := `<deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list><process id="p1" currentdbname="tenant_a"/><process id="p2" currentdbname="tenant_b"/></process-list><resource-list/></deadlock>`
		_, _, err = appender.Append([]xetrace.Event{{Name: xetrace.EventXMLDeadlockReport, Timestamp: time.Date(2026, 9, 24, 10, 11, 12, 0, time.UTC), DeadlockReportXML: report}})
		Expect(err).NotTo(HaveOccurred())
		var rows []recordstore.Row
		Expect(env.backend.Scan(env.ctx(), "deadlock-capture", 0, func(_ int64, row recordstore.Row) error {
			rows = append(rows, row)
			return nil
		})).To(Succeed())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]).To(HaveKey("deadlock"))
		Expect(rows[0]["deadlock"]).To(HaveKeyWithValue("xml", report))
		Expect(rows[0]["database"]).To(Equal("tenant_a"))
	})

	It("reports a malformed graph as an append error", func() {
		env := newEnvironment(nil)
		appender := recordAppender{store: env.store(), ctx: env.ctx(), stream: "bad-deadlock"}
		_, _, err := appender.Append([]xetrace.Event{{Name: xetrace.EventXMLDeadlockReport, Timestamp: capturedAt, DeadlockReportXML: "<deadlock/>"}})
		Expect(err).To(MatchError(ContainSubstring("lists no processes")))
	})

	It("stores no graph for ordinary SQL rows", func() {
		env := newEnvironment(nil)
		appender := recordAppender{store: env.store(), ctx: env.ctx(), stream: "statement-capture"}
		_, _, err := appender.Append([]xetrace.Event{{Name: xetrace.EventSQLStatementCompleted, Timestamp: capturedAt, Statement: "SELECT 1"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(env.backend.Scan(env.ctx(), "statement-capture", 0, func(_ int64, row recordstore.Row) error {
			Expect(row["deadlock"]).To(BeNil())
			return nil
		})).To(Succeed())
	})
})
