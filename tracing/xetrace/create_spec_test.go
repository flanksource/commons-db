package xetrace

import (
	"context"
	"fmt"
	"strings"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// containsMatcher accepts a statement that contains the expected fragment and
// records every statement it matched, in order.
func containsMatcher(received *[]string) sqlmock.QueryMatcher {
	return sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		if !strings.Contains(actual, expected) {
			return fmt.Errorf("%q does not contain %q", actual, expected)
		}
		if received != nil {
			*received = append(*received, actual)
		}
		return nil
	})
}

// grantedPermissionRows is the permission probe's answer for a sysadmin.
func grantedPermissionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"login", "product_major_version", "is_sysadmin",
		"alter_any_event_session", "create_any_event_session", "alter_any_event_session_enable",
		"drop_any_event_session", "view_server_state", "view_server_performance_state",
	}).AddRow("sa", 14, 1, nil, nil, nil, nil, nil, nil)
}

var _ = Describe("Create", func() {
	It("keeps statement filtering separate from the XE events sent to SQL Server", func() {
		var received []string
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(&received)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })

		mock.ExpectQuery("SELECT @@SPID").WillReturnRows(sqlmock.NewRows([]string{"spid"}).AddRow(57))
		mock.ExpectQuery("SELECT DB_NAME()").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("warehouse"))
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(grantedPermissionRows())
		mock.ExpectExec("CREATE EVENT SESSION [trace_spec] ON SERVER").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("ALTER EVENT SESSION [trace_spec] ON SERVER STATE = START").WillReturnResult(sqlmock.NewResult(0, 0))

		session, err := Create(context.Background(), pool, CreateOptions{
			Name: "trace_spec", Hosts: []string{"cycle*"}, Filter: EventFilter{Types: []string{"DDL"}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
		Expect(received).To(HaveLen(5), "@@SPID, DB_NAME(), the permission probe, the CREATE and the START")
		Expect(session.Statements).To(Equal(received[3:]))
		Expect(session.Statements[0]).To(ContainSubstring("like_i_sql_unicode_string(sqlserver.client_hostname, N'cycle%')"))
		Expect(session.Statements[0]).To(ContainSubstring("sqlserver.session_id <> 57"), "the reader's own connection is excluded")
		Expect(session.Statements[0]).To(ContainSubstring("equal_i_sql_unicode_string(sqlserver.database_name, N'warehouse')"),
			"no Databases scopes to the connection's database")
		Expect(session.Databases()).To(Equal([]string{"warehouse"}))
		Expect(session.FinalDelay()).To(Equal(DispatchLatency+dispatchMargin),
			"a final read waits out the session's own dispatch latency")
		Expect(session.opts.Events).To(Equal(DefaultEvents))
		Expect(session.Statements[0]).NotTo(ContainSubstring("ADD EVENT sqlserver.object_created"))
	})

	It("captures the whole instance for Databases [*] without asking for DB_NAME()", func() {
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(nil)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })

		mock.ExpectQuery("SELECT @@SPID").WillReturnRows(sqlmock.NewRows([]string{"spid"}).AddRow(57))
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(grantedPermissionRows())
		mock.ExpectExec("CREATE EVENT SESSION [trace_all] ON SERVER").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("ALTER EVENT SESSION [trace_all] ON SERVER STATE = START").WillReturnResult(sqlmock.NewResult(0, 0))

		session, err := Create(context.Background(), pool, CreateOptions{Name: "trace_all", Databases: []string{"*"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(mock.ExpectationsWereMet()).To(Succeed())
		Expect(session.Statements[0]).NotTo(ContainSubstring("sqlserver.database_name, N'"))
	})

	It("refuses a session without a name", func() {
		pool, _, err := sqlmock.New()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })

		_, err = Create(context.Background(), pool, CreateOptions{})

		Expect(err).To(MatchError(ContainSubstring("name")))
	})
})
