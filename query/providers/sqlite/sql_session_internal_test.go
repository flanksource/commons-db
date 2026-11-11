package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/sqlactivity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func sessionMock() (*sql.DB, sqlmock.Sqlmock) {
	pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { mock.ExpectClose(); Expect(pool.Close()).To(Succeed()) })
	return pool, mock
}

var _ = Describe("server session identity", func() {
	DescribeTable("reads the session the pinned connection is on",
		func(dialect sqlDialect, queryID string, columns []string, row []any, want query.ServerSession) {
			pool, mock := sessionMock()
			values := make([]driver.Value, len(row))
			for i, value := range row {
				values[i] = value
			}
			mock.ExpectQuery(".").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			var recorded []query.ServerSession
			diagnostics := query.NewDiagnostics(query.DiagnosticOptions{OnSession: func(s query.ServerSession) { recorded = append(recorded, s) }})
			conn, session, err := pinSession(context.Background(), pool, sessionRequest{Dialect: dialect, Diagnostics: diagnostics, Page: 2, QueryID: queryID})
			Expect(err).NotTo(HaveOccurred())
			Expect(conn.Close()).To(Succeed())
			Expect(session).To(Equal(want))
			Expect(recorded).To(Equal([]query.ServerSession{want}))
		},
		Entry("SQL Server SPID", dialectSQLServer, "", []string{"spid", "server", "database", "login"}, []any{int64(57), "sql-a", "warehouse", "etl"},
			query.ServerSession{Engine: "sqlserver", SessionID: 57, Server: "sql-a", Database: "warehouse", User: "etl", Page: 2}),
		Entry("MySQL connection id", dialectMySQL, "", []string{"id", "server", "database", "user"}, []any{int64(9), "mysql-a", "shop", "app@%"},
			query.ServerSession{Engine: "mysql", SessionID: 9, Server: "mysql-a", Database: "shop", User: "app@%", Page: 2}),
		Entry("ClickHouse query id", dialectClickHouse, "q-1", []string{"server", "database", "user"}, []any{"ch-a", "events", "reader"},
			query.ServerSession{Engine: "clickhouse", QueryID: "q-1", Server: "ch-a", Database: "events", User: "reader", Page: 2}),
	)

	It("refuses a statement on a session it could not identify", func() {
		pool, mock := sessionMock()
		mock.ExpectQuery(".").WillReturnError(sql.ErrConnDone)
		_, _, err := pinSession(context.Background(), pool, sessionRequest{Dialect: dialectPostgres, Diagnostics: query.NewDiagnostics(query.DiagnosticOptions{})})
		Expect(err).To(MatchError(ContainSubstring("identify the postgres session")))
	})
})

var _ = Describe("auto diagnose", func() {
	It("stops sampling with the grant a SQL Server login needs when its session is not visible", func() {
		pool, mock := sessionMock()
		mock.ExpectQuery(".").WillReturnRows(sqlmock.NewRows([]string{"sampled_at"}).AddRow(time.Now()), sqlmock.NewRows([]string{"session_id"}))
		errs := make(chan error, 1)
		var samples int
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{
			AutoDiagnose:  time.Millisecond,
			OnSample:      func(sqlactivity.Sample) { samples++ },
			OnSampleError: func(err error) { errs <- err },
		})
		stop := startAutoDiagnose(context.Background(), pool, query.ServerSession{Engine: "sqlserver", SessionID: 57}, diagnostics)
		Eventually(errs).Should(Receive(MatchError(ContainSubstring("VIEW SERVER STATE"))))
		stop()
		Expect(samples).To(BeZero())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	It("never samples an engine it cannot read, or before AutoDiagnose has passed", func() {
		pool, mock := sessionMock()
		diagnostics := query.NewDiagnostics(query.DiagnosticOptions{AutoDiagnose: time.Hour})
		startAutoDiagnose(context.Background(), pool, query.ServerSession{Engine: "postgres", SessionID: 41}, diagnostics)()
		startAutoDiagnose(context.Background(), pool, query.ServerSession{Engine: "mysql", SessionID: 9},
			query.NewDiagnostics(query.DiagnosticOptions{AutoDiagnose: time.Millisecond}))()
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})
