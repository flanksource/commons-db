package xetrace

import (
	"context"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/flanksource/sqlglot-go/sqlglot"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The exact Extended Events DDL every statement kind sends to SQL Server. The
// session name carries the `]` that closes a T-SQL name and the values carry
// the `'` that closes a string, so each expectation also pins the escaping.
const (
	specSessionName = "acme]o'trace"
	specActions     = "ACTION (package0.event_sequence, sqlserver.client_app_name, sqlserver.client_hostname, " +
		"sqlserver.database_name, sqlserver.session_id, sqlserver.sql_text, sqlserver.username)"
	specFilters = "(sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'warehouse')) AND " +
		"(NOT (sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'o''brien'))) AND " +
		"(sqlserver.like_i_sql_unicode_string(sqlserver.client_hostname, N'app[_]%')) AND sqlserver.session_id <> 55"
)

func specRingOptions() CreateOptions {
	return CreateOptions{
		Name: specSessionName, Databases: []string{"warehouse", "!o'brien"}, Hosts: []string{"app_*"},
		ExcludeSessionID: 55, MinDurationMicros: 1000,
		Events:      []string{EventRPCCompleted, EventObjectCreated, EventXMLDeadlockReport},
		MaxMemoryKB: 4096, MaxEvents: 1000,
	}
}

var _ = Describe("Extended Events DDL", func() {
	It("writes a ring_buffer session with every predicate, action and option", func() {
		Expect(BuildCreateSQL(specRingOptions())).To(Equal(
			"CREATE EVENT SESSION [acme]]o'trace] ON SERVER " +
				"ADD EVENT sqlserver.object_created (SET collect_database_name = (1) " + specActions +
				" WHERE (ddl_phase = 1 AND database_id <> 2 AND object_type <> 21587 AND " + specFilters + ")), " +
				"ADD EVENT sqlserver.rpc_completed (" + specActions +
				" WHERE (duration >= 1000 AND " + specFilters + ")), " +
				"ADD EVENT sqlserver.xml_deadlock_report " +
				"ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 1000) " +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = OFF, STARTUP_STATE = OFF)"))
	})

	It("reads back as the statement it was built as", func() {
		ddl, err := BuildCreateSQL(specRingOptions())
		Expect(err).NotTo(HaveOccurred())
		tree, err := sqlglot.ParseOne(ddl, "tsql")
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Class).To(Equal("CreateEventSession"))
		Expect(tree.This().Name()).To(Equal(specSessionName), "the `]` is part of the one session name, not the end of it")
		Expect(sqlglot.Generate(tree, "tsql")).To(Equal(ddl))
	})

	It("writes an event_file session, escaping the quote in its path", func() {
		Expect(BuildCreateSQL(CreateOptions{
			Name: "s", Events: []string{EventSPStatementCompleted},
			File: &FileTarget{Path: `C:\xe\it's.xel`, MaxFileSizeMB: 256, MaxRolloverFiles: 10},
		})).To(Equal(
			"CREATE EVENT SESSION [s] ON SERVER " +
				"ADD EVENT sqlserver.sp_statement_completed (" + specActions + ") " +
				`ADD TARGET package0.event_file (SET filename = N'C:\xe\it''s.xel', max_file_size = 256, max_rollover_files = 10) ` +
				"WITH (MAX_DISPATCH_LATENCY = 1 SECONDS, TRACK_CAUSALITY = ON, STARTUP_STATE = OFF)"))
	})

	It("starts and drops the session it created by its escaped name", func() {
		var received []string
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(&received)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })
		mock.ExpectQuery("SELECT @@SPID").WillReturnRows(sqlmock.NewRows([]string{"spid"}).AddRow(55))
		mock.ExpectQuery("HAS_PERMS_BY_NAME").WillReturnRows(grantedPermissionRows())
		mock.ExpectExec("CREATE EVENT SESSION").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("STATE = START").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DROP EVENT SESSION").WillReturnResult(sqlmock.NewResult(0, 0))

		session, err := Create(context.Background(), pool, specRingOptions())
		Expect(err).NotTo(HaveOccurred())
		Expect(session.Drop(context.Background())).To(Succeed())

		Expect(mock.ExpectationsWereMet()).To(Succeed())
		Expect(received[3:]).To(Equal([]string{
			"ALTER EVENT SESSION [acme]]o'trace] ON SERVER STATE = START",
			"DROP EVENT SESSION [acme]]o'trace] ON SERVER",
		}))
	})

	It("replaces each event's reader exclusion on reconnect, skipping the deadlock report", func() {
		var received []string
		pool, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(containsMatcher(&received)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = pool.Close() })
		mock.ExpectQuery("SELECT @@SPID").WillReturnRows(sqlmock.NewRows([]string{"spid"}).AddRow(55))
		mock.ExpectExec("IF EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("IF EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("ring_buffer").WillReturnRows(sqlmock.NewRows([]string{"target_data"}).AddRow(nil))

		opts := specRingOptions()
		opts.ExcludeSessionID = 0
		session := &Session{Name: specSessionName, pool: pool, opts: opts, owned: true}
		_, err = session.Poll(context.Background())
		Expect(err).NotTo(HaveOccurred())

		Expect(mock.ExpectationsWereMet()).To(Succeed())
		Expect(session.opts.ExcludeSessionID).To(Equal(55))
		Expect(received[1]).To(Equal(
			"IF EXISTS(SELECT 1 FROM sys.server_event_session_events AS e JOIN sys.server_event_sessions AS s ON s.event_session_id = e.event_session_id " +
				"WHERE s.name = N'acme]o''trace' AND e.name = N'rpc_completed') " +
				"ALTER EVENT SESSION [acme]]o'trace] ON SERVER DROP EVENT sqlserver.rpc_completed; " +
				"ALTER EVENT SESSION [acme]]o'trace] ON SERVER ADD EVENT sqlserver.rpc_completed (" + specActions +
				" WHERE (duration >= 1000 AND " + specFilters + "))"))
		Expect(received[2]).To(ContainSubstring("e.name = N'object_created'"))
	})
})
