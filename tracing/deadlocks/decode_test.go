package deadlocks_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/deadlocks"
)

// participant is the part of a Process a spec compares whole; frames and the
// input buffer are long enough to be asserted on their own.
type participant struct {
	ID, Host, App, Login, Database, Isolation, TransactionName, LockMode, WaitResource, Status string
	Victim                                                                                     bool
	SPID, TranCount                                                                            int
	WaitTimeMs, LogUsed                                                                        int64
}

func participants(processes []deadlocks.Process) []participant {
	out := make([]participant, len(processes))
	for i, p := range processes {
		out[i] = participant{
			ID: p.ID, Host: p.Host, App: p.ClientApp, Login: p.Login, Database: p.Database,
			Isolation: p.IsolationLevel, TransactionName: p.TransactionName, LockMode: p.LockMode,
			WaitResource: p.WaitResource, Status: p.Status, Victim: p.Victim,
			SPID: p.SPID, TranCount: p.TranCount, WaitTimeMs: p.WaitTimeMs, LogUsed: p.LogUsed,
		}
	}
	return out
}

const (
	jdbc     = "Microsoft JDBC Driver for SQL Server"
	labDB    = "warehouse"
	activity = "warehouse.dbo.AsActivity"
	ix15Hobt = int64(72057594747027456)
	ux1Hobt  = int64(72057594682015744)
)

var reportedAt = time.Date(2026, 9, 10, 16, 47, 50, 43_000_000, time.UTC)

var _ = Describe("DecodeReport", func() {
	It("decodes a two-process key-lookup deadlock into its participants and lock resources", func() {
		graph, err := deadlocks.DecodeReport(reportedAt, fixture("key-lookup-two-process.xml"))
		Expect(err).NotTo(HaveOccurred())

		Expect(graph.ID).To(Equal("20260910T164750043Z-processe80b448c8"))
		Expect(graph.Timestamp).To(Equal(reportedAt))
		Expect(graph.Database).To(Equal(labDB))
		Expect(graph.Victims).To(Equal(1))
		Expect(graph.Participants).To(Equal(2))
		Expect(graph.VictimHost).To(Equal("app-0"))
		Expect(graph.VictimApp).To(Equal(jdbc))
		Expect(graph.VictimStatement).To(Equal(
			"SELECT ACTIVITYGUID, ACTIVEFROMDATE, ACTIVETODATE, ACTIVITYGMT, CLIENTNUMBER, " +
				"CREATIONGMT, EFFECTIVEDATE, ENTRYGMT, ERRORSTATUSCODE, ORIGINALACTIVITYGUID, POLICYGUID, PROCESSINGORDER, " +
				"RELATEDGUID, SCHEDULEGUID, STATUSCODE, SUBSTATUSCODE, SUSPENSESTATUSCODE, TRANSACTIONGUID, TYPECODE, " +
				"XMLDATA FROM AsActivity WHERE (ACTIVITYGUID = @P0)"))
		Expect(graph.XML).To(HavePrefix("<deadlock>"))

		Expect(participants(graph.Processes)).To(Equal([]participant{
			{
				ID: "processe80b448c8", Victim: true, SPID: 93, Status: "suspended",
				Host: "app-0", App: jdbc, Login: "sa", Database: labDB, Isolation: "read committed (2)",
				TransactionName: "SELECT", TranCount: 0, LockMode: "S",
				WaitResource: "KEY: 5:72057594747027456 (a11aea7fd269)", WaitTimeMs: 3689, LogUsed: 0,
			},
			{
				ID: "processe8041aca8", SPID: 91, Status: "suspended",
				Host: "app-0", App: jdbc, Login: "sa", Database: labDB, Isolation: "read committed (2)",
				TransactionName: "implicit_transaction", TranCount: 2, LockMode: "X",
				WaitResource: "KEY: 5:72057594682015744 (f7d28d0730ab)", WaitTimeMs: 3684, LogUsed: 9292,
			},
		}))
		Expect(graph.Processes[0].Frames).To(Equal([]deadlocks.Frame{
			{ProcName: "adhoc", Line: 1, StmtStart: intPtr(40), StmtEnd: intPtr(708), SQLHandle: "0x02000000ca0750065d2aea6f2c86c75bb61da2a71394fbc10000000000000000000000000000000000000000", Text: "unknown"},
			{ProcName: "unknown", Line: 1, SQLHandle: "0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", Text: "unknown"},
		}))
		Expect(graph.Processes[1].InputBuffer).To(HavePrefix("(@P0 nvarchar(4000),@P1 datetime2"))
		Expect(graph.Processes[1].InputBuffer).To(ContainSubstring("UpdatedGMT < @P9 ;"),
			"the &lt; entity in the report decodes to <")

		Expect(graph.Resources).To(Equal([]deadlocks.Resource{
			{
				Kind: "keylock", LockID: "lockebbcf5580", Mode: "X", DatabaseID: 5,
				ObjectName: activity, Object: "AsActivity", Index: "IX_15_ASACTIVITY", HobtID: ix15Hobt,
				Resolution: deadlocks.ResolutionNamed,
				Owners:     []deadlocks.LockRequest{{ProcessID: "processe8041aca8", Mode: "X"}},
				Waiters:    []deadlocks.LockRequest{{ProcessID: "processe80b448c8", Mode: "S", RequestType: "wait"}},
				Attrs: map[string]string{
					"hobtid": "72057594747027456", "dbid": "5", "objectname": activity, "indexname": "IX_15_ASACTIVITY",
					"id": "lockebbcf5580", "mode": "X", "associatedObjectId": "72057594747027456",
				},
			},
			{
				Kind: "keylock", LockID: "lockf3d392f80", Mode: "S", DatabaseID: 5,
				ObjectName: activity, Object: "AsActivity", Index: "UX_1_ASACTIVITY", HobtID: ux1Hobt,
				Resolution: deadlocks.ResolutionNamed,
				Owners:     []deadlocks.LockRequest{{ProcessID: "processe80b448c8", Mode: "S"}},
				Waiters:    []deadlocks.LockRequest{{ProcessID: "processe8041aca8", Mode: "X", RequestType: "wait"}},
				Attrs: map[string]string{
					"hobtid": "72057594682015744", "dbid": "5", "objectname": activity, "indexname": "UX_1_ASACTIVITY",
					"id": "lockf3d392f80", "mode": "S", "associatedObjectId": "72057594682015744",
				},
			},
		}))
	})

	It("merges the per-victim repeats of one lock into a single resource with every request on it", func() {
		graph, err := deadlocks.DecodeReport(reportedAt, fixture("key-lookup-seven-victims.xml"))
		Expect(err).NotTo(HaveOccurred())

		Expect(graph.ID).To(Equal("20260910T164750043Z-processf700de4e8"))
		Expect(graph.Victims).To(Equal(7))
		Expect(graph.Participants).To(Equal(8))

		type merged struct {
			LockID          string
			Owners, Waiters []deadlocks.LockRequest
		}
		got := make([]merged, len(graph.Resources))
		for i, r := range graph.Resources {
			got[i] = merged{LockID: r.LockID, Owners: r.Owners, Waiters: r.Waiters}
		}
		wait := func(id, mode string) deadlocks.LockRequest {
			return deadlocks.LockRequest{ProcessID: id, Mode: mode, RequestType: "wait"}
		}
		granted := func(id, mode string) deadlocks.LockRequest {
			return deadlocks.LockRequest{ProcessID: id, Mode: mode}
		}
		Expect(got).To(Equal([]merged{
			{
				LockID: "lockf3d3f4d80",
				Owners: []deadlocks.LockRequest{wait("processe80b55088", "S"), granted("processe80078ca8", "X")},
				Waiters: []deadlocks.LockRequest{
					wait("processf700de4e8", "S"), wait("processf34705088", "S"), wait("processe8041aca8", "S"),
					wait("processf347044e8", "S"), wait("processf51a2e108", "S"), wait("processee8c36108", "S"),
					wait("processe80b55088", "S"),
				},
			},
			{
				LockID: "lockf1d2d5980",
				Owners: []deadlocks.LockRequest{
					granted("processf700de4e8", "S"), granted("processf34705088", "S"), granted("processf347044e8", "S"),
					granted("processee8c36108", "S"), granted("processe80b55088", "S"),
				},
				Waiters: []deadlocks.LockRequest{wait("processe80078ca8", "X")},
			},
		}))
	})

	It("keeps a resource kind it does not interpret, with all of its attributes", func() {
		report := `<deadlock><victim-list><victimProcess id="p1"/></victim-list>` +
			`<process-list><process id="p1" spid="51" currentdbname="db"/><process id="p2" spid="52" currentdbname="db"/></process-list>` +
			`<resource-list><exchangeEvent id="Pipe1" WaitType="e_waitPipeGetRow" nodeId="3">` +
			`<owner-list><owner id="p2"/></owner-list><waiter-list><waiter id="p1"/></waiter-list></exchangeEvent></resource-list></deadlock>`

		graph, err := deadlocks.DecodeReport(reportedAt, report)
		Expect(err).NotTo(HaveOccurred())
		Expect(graph.Resources).To(Equal([]deadlocks.Resource{{
			Kind: "exchangeEvent", LockID: "Pipe1", Resolution: deadlocks.ResolutionNotApplicable,
			Owners:  []deadlocks.LockRequest{{ProcessID: "p2"}},
			Waiters: []deadlocks.LockRequest{{ProcessID: "p1"}},
			Attrs:   map[string]string{"id": "Pipe1", "WaitType": "e_waitPipeGetRow", "nodeId": "3"},
		}}))
	})

	DescribeTable("refuses a report it cannot attribute",
		func(report, message string) {
			_, err := deadlocks.DecodeReport(reportedAt, report)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a root other than <deadlock>", `<event name="x"/>`, "<deadlock>"),
		Entry("no processes", `<deadlock><victim-list/><process-list/><resource-list/></deadlock>`, "no processes"),
		Entry("a victim that is not a participant",
			`<deadlock><victim-list><victimProcess id="ghost"/></victim-list><process-list><process id="p1" spid="1"/></process-list><resource-list/></deadlock>`,
			"ghost"),
		Entry("a non-numeric spid",
			`<deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list><process id="p1" spid="x"/></process-list><resource-list/></deadlock>`,
			"spid"),
		Entry("a non-numeric stmtstart", withFrame(`stmtstart="x" stmtend="20"`, "SELECT 1"), `stmtstart "x" is not a number`),
		Entry("an odd stmtend, which is not a UTF-16 byte offset", withFrame(`stmtstart="0" stmtend="15"`, "SELECT 1"), "stmtend=15"),
		Entry("a stmtend before its stmtstart", withFrame(`stmtstart="20" stmtend="10"`, "SELECT 1"), "stmtend=10"),
		Entry("a negative stmtstart", withFrame(`stmtstart="-2" stmtend="10"`, "SELECT 1"), "stmtstart=-2"),
	)

	type running struct {
		Statement string
		Length    int
		Truncated bool
	}
	DescribeTable("slices the running statement out of the batch by the top frame's UTF-16 byte offsets",
		func(offsets, procName, inputBuffer string, want running) {
			graph, err := deadlocks.DecodeReport(reportedAt, withProcFrame(procName, offsets, inputBuffer))
			Expect(err).NotTo(HaveOccurred())
			p := graph.Processes[0]
			Expect(running{p.Statement, p.StatementLength, p.InputBufferTruncated}).To(Equal(want))
		},
		// "(@P0 int)" is 9 characters, so SELECT starts at byte 18; "SELECT 1"
		// ends at character 16, byte 32, which stmtend includes.
		Entry("a statement inside a prepared batch", `stmtstart="18" stmtend="32"`, "adhoc",
			"(@P0 int)SELECT 1;SELECT @P0", running{"SELECT 1", 8, false}),
		Entry("stmtend -1 runs to the end of the batch", `stmtstart="36" stmtend="-1"`, "adhoc",
			"(@P0 int)SELECT 1;SELECT @P0", running{"SELECT @P0", 10, false}),
		Entry("a missing stmtend runs to the end of the batch", `stmtstart="36"`, "adhoc",
			"(@P0 int)SELECT 1;SELECT @P0", running{"SELECT @P0", 10, false}),
		Entry("no offsets: the whole batch", ``, "adhoc",
			"(@P0 int)SELECT 1;SELECT @P0", running{"(@P0 int)SELECT 1;SELECT @P0", 28, false}),
		// U+1F600 is two UTF-16 code units and four UTF-8 bytes, é one unit and two
		// bytes: the second SELECT starts at code unit 14, byte 28.
		Entry("a multibyte character before the statement", `stmtstart="28" stmtend="42"`, "adhoc",
			"SELECT N'\U0001F600é';SELECT 2", running{"SELECT 2", 8, false}),
		Entry("a statement running past the captured buffer", `stmtstart="18" stmtend="98"`, "adhoc",
			"(@P0 int)SELECT 1", running{"SELECT 1", 41, true}),
		Entry("a statement starting past the captured buffer", `stmtstart="100" stmtend="120"`, "adhoc",
			"(@P0 int)SELECT 1", running{"", 11, true}),
		Entry("a procedure frame, whose offsets index the module rather than the batch", `stmtstart="18" stmtend="32"`,
			"OIPA.dbo.usp_next", "EXEC dbo.usp_next 1", running{"EXEC dbo.usp_next 1", 19, false}),
	)

	It("dates each wait from the report timestamp, and leaves a process that was not waiting undated", func() {
		report := `<deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list>` +
			`<process id="p1" spid="51" waittime="1500"/><process id="p2" spid="52" waittime="0"/>` +
			`</process-list><resource-list/></deadlock>`
		graph, err := deadlocks.DecodeReport(reportedAt, report)
		Expect(err).NotTo(HaveOccurred())
		Expect([]*time.Time{graph.Processes[0].WaitingSince, graph.Processes[1].WaitingSince}).To(Equal([]*time.Time{
			timePtr(time.Date(2026, 9, 10, 16, 47, 48, 543_000_000, time.UTC)), nil,
		}))
	})
})

// withFrame is a one-process report whose top frame is an ad hoc batch with
// the given offset attributes.
func withFrame(offsets, inputBuffer string) string {
	return withProcFrame("adhoc", offsets, inputBuffer)
}

func withProcFrame(procName, offsets, inputBuffer string) string {
	return `<deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list><process id="p1" spid="51">` +
		`<executionStack><frame procname="` + procName + `" line="1" ` + offsets + `>unknown</frame></executionStack>` +
		`<inputbuf>` + inputBuffer + `</inputbuf></process></process-list><resource-list/></deadlock>`
}

var _ = Describe("DecodeEvent", func() {
	It("takes the timestamp and the report from the xml_deadlock_report event envelope", func() {
		report := strings.TrimSpace(fixture("key-lookup-two-process.xml"))
		event := `<event name="xml_deadlock_report" package="sqlserver" timestamp="2026-09-10T16:47:50.043Z">` +
			`<data name="xml_report"><type name="xml" package="package0"></type><value>` + report + `</value></data></event>`

		graph, err := deadlocks.DecodeEvent(event)
		Expect(err).NotTo(HaveOccurred())
		Expect(graph.Timestamp).To(Equal(reportedAt))
		Expect(graph.ID).To(Equal("20260910T164750043Z-processe80b448c8"))
		Expect(graph.XML).To(Equal(report))
	})

	It("refuses an event that is not a deadlock report", func() {
		_, err := deadlocks.DecodeEvent(`<event name="wait_info" timestamp="2026-09-10T16:47:50.043Z"></event>`)
		Expect(err).To(MatchError(ContainSubstring("wait_info")))
	})
})

var _ = Describe("ParseID", func() {
	It("round-trips the id Decode gives a deadlock", func() {
		at, victim, err := deadlocks.ParseID("20260910T164750043Z-processe80b448c8")
		Expect(err).NotTo(HaveOccurred())
		Expect(at).To(Equal(reportedAt))
		Expect(victim).To(Equal("processe80b448c8"))
	})

	It("refuses an id without a timestamp", func() {
		_, _, err := deadlocks.ParseID("processe80b448c8")
		Expect(err).To(HaveOccurred())
	})
})
