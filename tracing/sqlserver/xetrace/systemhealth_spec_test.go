package xetrace

import (
	"context"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("system_health attachment", func() {
	It("baselines the existing file and never drops the built-in session", func() {
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(nil)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })

		current := `/var/opt/mssql/log/system_health_0_1337.xel`
		pattern := `/var/opt/mssql/log/system_health*.xel`
		mock.ExpectQuery("sys.dm_xe_session_targets").WillReturnRows(
			sqlmock.NewRows([]string{"current_file", "dispatch_latency_ms"}).AddRow(current, 30_000))
		// Only the current file is read for the baseline: the newest event is in
		// it, and the rolled-over files before it can hold hundreds of MB.
		mock.ExpectQuery("sys.fn_xe_file_target_read_file").WithArgs(current).WillReturnRows(
			sqlmock.NewRows([]string{"file_name", "file_offset"}).AddRow(current, 4096))
		mock.ExpectQuery("sys.dm_xe_sessions WHERE name").WithArgs(SystemHealthSession).WillReturnRows(
			sqlmock.NewRows([]string{"dropped_event_count", "dropped_buffer_count"}).AddRow(7, 0))

		session, err := AttachSystemHealth(context.Background(), pool, CreateOptions{Session: SystemHealthSession})

		Expect(err).NotTo(HaveOccurred())
		Expect(session.Name).To(Equal(SystemHealthSession))
		Expect(session.DispatchLatency()).To(Equal(30 * time.Second))
		Expect(session.FinalDelay()).To(Equal(30*time.Second + dispatchMargin))
		Expect(session.fileCursor).To(Equal(fileCursor{file: current, offset: 4096, valid: true}))
		Expect(session.filePattern).To(Equal(pattern), "later reads follow the session across its rollover files")
		Expect(session.Drop(context.Background())).To(Succeed())
		Expect(mock.ExpectationsWereMet()).To(Succeed(), "Drop must not issue DROP EVENT SESSION for system_health")
	})
})
