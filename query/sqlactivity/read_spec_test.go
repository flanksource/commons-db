package sqlactivity

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var serverTime = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

func started(ago time.Duration) time.Time { return serverTime.Add(-ago) }

var postgresColumns = []string{"pid", "blocked_by", "datname", "usename", "application_name", "client_host", "state",
	"wait_event_type", "wait_event", "query_start", "xact_start", "query_id", "query"}

var sqlServerColumns = []string{"session_id", "request_id", "blocked_by", "database_name", "login_name", "program_name", "host_name",
	"state", "command", "wait_type", "wait_resource", "wait_time", "started_at", "transaction_started_at", "total_elapsed_time",
	"cpu_time", "logical_reads", "reads", "writes", "memory_bytes", "open_transaction_count", "query_hash", "sql_text"}

func mockDB(matcher sqlmock.QueryMatcher) (*sql.DB, sqlmock.Sqlmock) {
	pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { mock.ExpectClose(); Expect(pool.Close()).To(Succeed()) })
	return pool, mock
}

var _ = Describe("reading Postgres activity", func() {
	It("reads the server time and the activity view in one read-only transaction", func() {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectBegin()
		mock.ExpectQuery(`commons-db:sqlactivity \*/ SELECT now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(serverTime))
		mock.ExpectQuery(`FROM pg_stat_activity`).WithArgs(true, int64(0)).WillReturnRows(sqlmock.NewRows(postgresColumns).
			AddRow(41, 40, "orders", "app", "checkout", "10.0.0.5", "active", "Lock", "transactionid",
				started(2*time.Second), started(3*time.Second), "-7230", "UPDATE orders SET paid = true").
			AddRow(40, 0, "orders", "app", "checkout", "", "idle in transaction", "", "", started(time.Minute), started(time.Minute), "", ""))
		mock.ExpectRollback()
		got, err := readPostgres(context.Background(), pool, Filter{Idle: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(Snapshot{ServerTime: serverTime, Rows: []Sample{
			{SessionID: 41, BlockedBy: 40, Database: "orders", User: "app", Application: "checkout", ClientHost: "10.0.0.5",
				State: "active", WaitType: "Lock:transactionid", StartedAt: started(2 * time.Second), TransactionStartedAt: started(3 * time.Second),
				OpenTransactions: 1, QueryID: "-7230", Command: "UPDATE", SQL: "UPDATE orders SET paid = true"},
			{SessionID: 40, Database: "orders", User: "app", Application: "checkout", State: "idle in transaction",
				StartedAt: started(time.Minute), TransactionStartedAt: started(time.Minute), OpenTransactions: 1},
		}}))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("excludes the poller's own backend and, unless asked, idle sessions, and narrows to one session when asked", func() {
		Expect(postgresActivity).To(ContainSubstring("a.pid <> pg_backend_pid()"))
		Expect(postgresActivity).To(ContainSubstring("a.state IS DISTINCT FROM 'idle' OR $1"))
		Expect(postgresActivity).To(ContainSubstring("a.backend_type = 'client backend'"))
		Expect(postgresActivity).To(ContainSubstring("$2 = 0 OR a.pid = $2"))
	})
	It("binds the session filter", func() {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(serverTime))
		mock.ExpectQuery(`FROM pg_stat_activity`).WithArgs(false, int64(4121)).WillReturnRows(sqlmock.NewRows(postgresColumns))
		mock.ExpectRollback()
		_, err := readPostgres(context.Background(), pool, Filter{SessionID: 4121})
		Expect(err).NotTo(HaveOccurred())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("surfaces a failed activity read", func() {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(serverTime))
		mock.ExpectQuery(`FROM pg_stat_activity`).WillReturnError(sql.ErrConnDone)
		mock.ExpectRollback()
		_, err := readPostgres(context.Background(), pool, Filter{})
		Expect(err).To(MatchError(ContainSubstring("read pg_stat_activity")))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
})

var _ = Describe("reading SQL Server requests", func() {
	It("reads the server time and every request in one marked batch with the idle switch and session named", func() {
		var statement string
		pool, mock := mockDB(sqlmock.QueryMatcherFunc(func(_, actual string) error { statement = actual; return nil }))
		mock.ExpectQuery("batch").WithArgs(sql.Named("idle", false), sql.Named("session", int64(0))).WillReturnRows(
			sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime),
			sqlmock.NewRows(sqlServerColumns).
				AddRow(55, 0, 54, "warehouse", "etl", "loader", "host-a", "suspended", "UPDATE", "LCK_M_X", "KEY: 5:72057594043236352 (8194443284a0)",
					1200, started(1300*time.Millisecond), started(5*time.Second), 1300, 40, 900, 12, 3, 16384, 1, []byte{0xab, 0x01}, "UPDATE dbo.ledger SET v = 1").
				AddRow(54, 0, 0, "warehouse", "etl", "loader", "host-a", "sleeping", "", "", "", 0, started(time.Minute), started(time.Minute), nil, 0, 0, 0, 0, 0, 1, nil, "BEGIN TRAN"),
		)
		got, err := readSQLServer(context.Background(), pool, Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(Snapshot{ServerTime: serverTime, Rows: []Sample{
			{SessionID: 55, BlockedBy: 54, Database: "warehouse", User: "etl", Application: "loader", ClientHost: "host-a",
				State: "suspended", Command: "UPDATE", WaitType: "LCK_M_X", WaitResource: "KEY: 5:72057594043236352 (8194443284a0)", WaitMs: 1200,
				StartedAt: started(1300 * time.Millisecond), TransactionStartedAt: started(5 * time.Second), ElapsedMs: 1300, CPUMs: 40,
				LogicalReads: 900, Reads: 12, Writes: 3, MemoryBytes: 16384, OpenTransactions: 1, QueryID: "0xAB01", SQL: "UPDATE dbo.ledger SET v = 1"},
			{SessionID: 54, Database: "warehouse", User: "etl", Application: "loader", ClientHost: "host-a", State: "sleeping",
				StartedAt: started(time.Minute), TransactionStartedAt: started(time.Minute), OpenTransactions: 1, SQL: "BEGIN TRAN"},
		}}))
		Expect(strings.TrimSpace(statement)).To(HavePrefix(pollMarker))
		Expect(statement).To(ContainSubstring("s.session_id <> @@SPID"))
		Expect(statement).To(ContainSubstring("s.open_transaction_count > 0 OR @idle = 1"))
		Expect(statement).To(ContainSubstring("@session = 0 OR s.session_id = @session"))
		Expect(statement).To(ContainSubstring("statement_start_offset"))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	It("names the session a filter narrows to", func() {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectQuery(".").WithArgs(sql.Named("idle", true), sql.Named("session", int64(57))).WillReturnRows(
			sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime), sqlmock.NewRows(sqlServerColumns))
		_, err := readSQLServer(context.Background(), pool, Filter{Idle: true, SessionID: 57})
		Expect(err).NotTo(HaveOccurred())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})
	DescribeTable("refuses an incomplete batch", func(resultSets func() []*sqlmock.Rows, message string) {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectQuery(".").WillReturnRows(resultSets()...)
		_, err := readSQLServer(context.Background(), pool, Filter{})
		Expect(err).To(MatchError(ContainSubstring(message)))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	},
		Entry("no server time", func() []*sqlmock.Rows { return []*sqlmock.Rows{sqlmock.NewRows([]string{"sampled_at"})} }, "no server timestamp"),
		Entry("no requests", func() []*sqlmock.Rows {
			return []*sqlmock.Rows{sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime)}
		}, "no requests result set"),
		Entry("two server times", func() []*sqlmock.Rows {
			return []*sqlmock.Rows{sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime).AddRow(serverTime), sqlmock.NewRows(sqlServerColumns)}
		}, "multiple server timestamps"),
	)
})

var _ = Describe("Read", func() {
	It("stamps every row with its engine and derives its times from the server time", func() {
		pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
		mock.ExpectQuery(".").WillReturnRows(sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime),
			sqlmock.NewRows(sqlServerColumns).AddRow(55, 0, 54, "warehouse", "etl", "loader", "host-a", "suspended", "UPDATE", "", "",
				0, started(2*time.Second), started(5*time.Second), nil, 0, 0, 0, 0, 0, 1, nil, "UPDATE dbo.ledger SET v = 1"))
		got, err := Read(context.Background(), pool, EngineSQLServer, Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Rows).To(HaveLen(1))
		row := got.Rows[0]
		Expect([]any{row.Engine, row.SampledAt, row.ElapsedMs, row.TransactionMs, row.Blocked}).
			To(Equal([]any{EngineSQLServer, serverTime, float64(2000), float64(5000), true}))
	})
	It("refuses an engine it cannot read", func() {
		pool, _ := mockDB(sqlmock.QueryMatcherRegexp)
		_, err := Read(context.Background(), pool, "mysql", Filter{})
		Expect(err).To(MatchError(ContainSubstring(`engine "mysql"`)))
	})
})
