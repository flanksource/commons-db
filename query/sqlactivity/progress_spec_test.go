package sqlactivity

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL Server request progress", func() {
	It("gates each tier on the active request's age and joins waits by request as well as session", func() {
		var statement string
		pool, mock := mockDB(sqlmock.QueryMatcherFunc(func(_, actual string) error { statement = actual; return nil }))
		mock.ExpectQuery("batch").WithArgs(sql.Named("idle", false), sql.Named("session", int64(0)), sql.Named("progress", true), sql.Named("progress_ms", int64(60_000)), sql.Named("details_ms", int64(300_000))).WillReturnRows(
			sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime), sqlmock.NewRows(sqlServerColumns))
		_, err := Read(context.Background(), pool, EngineSQLServer, Filter{SQLServerProgress: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(statement).To(ContainSubstring("r.total_elapsed_time > @progress_ms"))
		Expect(statement).To(ContainSubstring("r.total_elapsed_time > @details_ms"))
		Expect(statement).To(ContainSubstring("t.session_id = r.session_id AND t.request_id = r.request_id"))
		Expect(statement).To(ContainSubstring("dt.transaction_id = progress.transaction_id"))
		Expect(mock.ExpectationsWereMet()).To(Succeed())
	})

	DescribeTable("decodes the observed measurements",
		func(command string, age time.Duration, columns []driver.Value, expected SQLServerProgress) {
			pool, mock := mockDB(sqlmock.QueryMatcherRegexp)
			base := []driver.Value{55, 2, 0, "orders", "app", "checkout", "host", "running", command, "", "", 0,
				started(age), nil, age.Milliseconds(), 0, 0, 0, 0, 0, 0, nil, ""}
			mock.ExpectQuery("commons-db:sqlactivity").WillReturnRows(sqlmock.NewRows([]string{"sampled_at"}).AddRow(serverTime),
				sqlmock.NewRows(sqlServerColumns).AddRow(append(base, columns...)...))
			got, err := Read(context.Background(), pool, EngineSQLServer, Filter{SQLServerProgress: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Rows).To(HaveLen(1))
			Expect(got.Rows[0].SQLServerProgress).To(Equal(expected))
		},
		Entry("strict 60-second boundary", "ROLLBACK", time.Minute, make([]driver.Value, 12), SQLServerProgress{}),
		Entry("rollback at zero percent", "KILLED/ROLLBACK", time.Minute+time.Millisecond,
			[]driver.Value{0.0, 0, "9007199254740993", 7, 0, nil, nil, nil, nil, nil, nil, nil},
			SQLServerProgress{PercentComplete: new(0.0), TransactionID: "9007199254740993", TransactionState: "rolling back", RowsReturned: new(int64(0))}),
		Entry("ordinary DML at the five-minute boundary", "UPDATE", 5*time.Minute,
			[]driver.Value{0.0, 0, "42", 2, 0, "WRITELOG", nil, nil, nil, nil, nil, nil},
			SQLServerProgress{TransactionID: "42", TransactionState: "active", RowsReturned: new(int64(0)), LastWaitType: "WRITELOG"}),
		Entry("partial rollback with deeper details", "ROLLBACK", 5*time.Minute+time.Millisecond,
			[]driver.Value{42.5, 120_000, "43", 7, 0, "WRITELOG", 400, 16_384, 8_192, 2, "WRITELOG", 2_500},
			SQLServerProgress{PercentComplete: new(42.5), EstimatedRemainingMs: new(120_000.0), TransactionID: "43", TransactionState: "rolling back",
				RowsReturned: new(int64(0)), LastWaitType: "WRITELOG", TransactionLogRecords: new(int64(400)), TransactionLogBytesUsed: new(int64(16_384)),
				TransactionLogBytesReserved: new(int64(8_192)), WaitingTaskCount: new(int64(2)), LongestTaskWaitType: "WRITELOG", LongestTaskWaitMs: new(2_500.0)}),
		Entry("no matching transaction or waiting tasks", "SELECT", 6*time.Minute,
			[]driver.Value{0.0, 0, nil, nil, 5, nil, nil, nil, nil, 0, nil, nil},
			SQLServerProgress{RowsReturned: new(int64(5)), WaitingTaskCount: new(int64(0))}),
	)
})
