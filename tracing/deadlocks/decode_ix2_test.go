package deadlocks_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/deadlocks"
)

// key-lookup-ix2-four-process.xml is a UAT capture from 2026-09-14, taken after
// AsActivity was reclustered on CX_ASACTIVITY_EFFDATE_GUID: the residual key
// lookup through IX_2_AsActivity. Three findNextPendingActivityDclByClientGuid
// readers hold, or queue for, S on an IX_2 entry and wait for the clustered
// row, which the status UPDATE holds X while it converts to X on IX_2. The
// tenant's database and login are scrubbed to warehouse_uat and app.
const (
	uatDB       = "warehouse_uat"
	uatActivity = "warehouse_uat.dbo.AsActivity"
	cxHobt      = int64(72057595309522944)
	ix2Hobt     = int64(72057595309719552)
	zeroHandle  = "0x0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
)

var ix2ReportedAt = time.Date(2026, 9, 14, 13, 46, 31, 365_000_000, time.UTC)

// reclusteredSchema is UAT's AsActivity when the graph was taken.
var reclusteredSchema = map[string]deadlocks.IndexType{
	"CX_ASACTIVITY_EFFDATE_GUID": deadlocks.IndexClustered,
	"IX_2_AsActivity":            deadlocks.IndexNonclustered,
}

// readerStatement is the part of the readers' 11.7k-character statement that
// survives SQL Server's 1,023-character input buffer: everything after the
// 35-character parameter declaration (stmtstart 70), up to the cut.
var readerStatement = strings.Join([]string{
	"SELECT AsActivity.ActivityGUID,",
	"              AsActivity.TransactionGUID,",
	"              AsActivity.TypeCode typeCode,",
	"              AsActivity.StatusCode,",
	"              AsActivity.EffectiveDate effectiveDate,",
	"              AsActivity.ActiveFromDate,",
	"              AsActivity.ActiveToDate,",
	"              AsActivity.ClientNumber,",
	"              AsActivity.PolicyGuid,",
	"              AsActivity.RelatedGuid,",
	"              AsActivity.XMLData activityXmlData,",
	"              AsActivity.ProcessingOrder processingOrder,",
	"              AsActivity.ErrorStatusCode,",
	"              AsActivity.SuspenseStatusCode,",
	"              AsActivity.ActivityGMT,",
	"              AsActivity.EntryGMT,",
	"              AsActivity.CreationGMT creationGMT,",
	"              AsActivity.OriginalActivityGUID",
	"         FROM AsActivity",
	"         JOIN AsClientActivity ON AsActivity.ActivityGuid = AsClientActivity.ActivityGuid",
	"         AND AsClientActivity.ClientGuid = @P0",
	"       WHERE AsActivity.StatusCode IN",
}, " \n")

var readerBatch = "(@P0 nvarchar(4000),@P1 date)      " + readerStatement

// writerStatement is the first of the writer's four statements: UTF-16 bytes
// 342 through 702 of its batch, i.e. characters 171 through 351.
const writerStatement = "UPDATE ASACTIVITY SET StatusCode = @P0  , ActiveFromDate = @P1  , ActivityGMT = @P2  , " +
	"XMLData = @P3  , ClientNumber = @P4  WHERE ACTIVITYGUID='92236A53-0953-4289-9A93-9B413BA78BE7'"

const writerBatch = "(@P0 nvarchar(4000),@P1 datetime2,@P2 datetime2,@P3 nvarchar(4000),@P4 nvarchar(4000)," +
	"@P5 nvarchar(4000),@P6 nvarchar(4000),@P7 datetime2,@P8 nvarchar(4000),@P9 datetime2)" + writerStatement +
	";UPDATE ASCHANGEREQUEST SET StatusCode = @P5  WHERE ChangeRequestGUID = 'A2DE41BE-FE0F-421C-9CEF-CE99B0E0C7E8'" +
	";UPDATE AsClientRelationship SET RecordStatusCode = @P6  WHERE ClientRelationshipGUID = '6A84F895-DF6D-4207-89AA-4BB9D29A5863'" +
	";UPDATE AsClient SET UpdatedGMT = @P7 WHERE ClientGUID = @P8 AND UpdatedGMT < @P9 ;"

func intPtr(v int) *int { return &v }

func timePtr(v time.Time) *time.Time { return &v }

// waitedFrom is 13:46:20.<ms> UTC: 13:46:31.365 less a process's waittime of
// about eleven seconds, worked out by hand for each process.
func waitedFrom(ms int) time.Time {
	return time.Date(2026, 9, 14, 13, 46, 20, ms*int(time.Millisecond), time.UTC)
}

var _ = Describe("DecodeReport on the reclustered schema", func() {
	It("decodes each process's running statement, the readers' truncated buffers and when each began waiting", func() {
		report := fixture("key-lookup-ix2-four-process.xml")
		graph, err := deadlocks.DecodeReport(ix2ReportedAt, report)
		Expect(err).NotTo(HaveOccurred())
		graph.Deadlock = withIndexTypes(graph, reclusteredSchema)
		deadlocks.Analyze(&graph.Deadlock)

		reader := func(id string, spid, tranCount int, name, started string, waitMs int64, since time.Time, stmtEnd, length int, handle string) deadlocks.Process {
			return deadlocks.Process{
				ID: id, Victim: true, SPID: spid, Status: "suspended", Host: "app-0", ClientApp: jdbc, Login: "app",
				Database: uatDB, IsolationLevel: "read committed (2)", TranCount: tranCount, TransactionName: name,
				LastTranStarted: started, LockMode: "S", WaitResource: "KEY: 8:72057595309522944 (fd6002a76cfd)", WaitTimeMs: waitMs,
				Frames: []deadlocks.Frame{
					{ProcName: "adhoc", Line: 1, StmtStart: intPtr(70), StmtEnd: intPtr(stmtEnd), SQLHandle: handle, Text: "unknown"},
					{ProcName: "unknown", Line: 1, SQLHandle: zeroHandle, Text: "unknown"},
				},
				InputBuffer: readerBatch, Statement: readerStatement, StatementLength: length, InputBufferTruncated: true,
				WaitingSince: timePtr(since),
			}
		}
		clusteredAttrs := map[string]string{
			"hobtid": "72057595309522944", "dbid": "8", "objectname": uatActivity, "indexname": "CX_ASACTIVITY_EFFDATE_GUID",
			"id": "lock2785d2bdf00", "mode": "X", "associatedObjectId": "72057595309522944",
		}

		Expect(graph).To(Equal(deadlocks.Graph{
			Deadlock: deadlocks.Deadlock{
				ID: "20260914T134631365Z-process2784a65e108", Timestamp: ix2ReportedAt, Database: uatDB,
				Shape:     deadlocks.ShapeKeyLookupVsWriter,
				Signature: "keylock AsActivity.CX_ASACTIVITY_EFFDATE_GUID X→S ⇄ keylock AsActivity.IX_2_AsActivity S→X",
				Objects:   []string{"AsActivity"}, Indexes: []string{"CX_ASACTIVITY_EFFDATE_GUID", "IX_2_AsActivity"},
				LockTypes: []string{"keylock"}, Victims: 3, Participants: 4,
				VictimHost: "app-0", VictimApp: jdbc,
				VictimStatement: "SELECT AsActivity.ActivityGUID, AsActivity.TransactionGUID, AsActivity.TypeCode typeCode, " +
					"AsActivity.StatusCode, AsActivity.EffectiveDate effectiveDate, AsActivity.ActiveFromDate, AsActivity.ActiveToDate, " +
					"AsActivity.ClientNumber, AsActivity.PolicyGuid, AsActivity.RelatedGuid, AsActivity.XMLData activityXmlData, " +
					"AsActivity.ProcessingOrder processingOrder, AsActivity.ErrorStatusCode, AsActivity.SuspenseStatusCode, " +
					"AsActivity.ActivityGMT, AsActivity.EntryGMT, AsActivity.CreationGMT creationGMT, AsActivity.OriginalActivityGUID " +
					"FROM AsActivity JOIN AsClientActivity ON AsActivity.ActivityGuid = AsClientActivity.ActivityGuid " +
					"AND AsClientActivity.ClientGuid = @P0 WHERE AsActivity.StatusCode IN",
				Processes: []deadlocks.Process{
					// (23414-70)/2+1, (25208-70)/2+1 and (25520-70)/2+1 characters.
					reader("process2784a65e108", 211, 0, "SELECT", "2026-09-14T15:46:20.303", 10957, waitedFrom(408), 23414, 11673,
						"0x02000000b91b281f109973881c0ed6bbd06da7abba13f9b00000000000000000000000000000000000000000"),
					reader("process275d4c63848", 212, 1, "implicit_transaction", "2026-09-14T15:46:01.080", 10953, waitedFrom(412), 25208, 12570,
						"0x02000000411ff933aec1ef7116d3d8ba7abd70fccaff3f670000000000000000000000000000000000000000"),
					reader("process27c135908c8", 199, 1, "implicit_transaction", "2026-09-14T15:46:09.930", 10965, waitedFrom(400), 25520, 12726,
						"0x02000000073b8f161f356c5ff2499ce8ec3ea218e353ff7e0000000000000000000000000000000000000000"),
					{
						ID: "process27c0b7528c8", SPID: 210, Status: "suspended", Host: "app-0", ClientApp: jdbc, Login: "app",
						Database: uatDB, IsolationLevel: "read committed (2)", TranCount: 2, TransactionName: "implicit_transaction",
						LastTranStarted: "2026-09-14T15:46:20.377", LockMode: "X", WaitResource: "KEY: 8:72057595309719552 (bdc70c38b08d)",
						WaitTimeMs: 10981, LogUsed: 3828,
						Frames: []deadlocks.Frame{
							{ProcName: "adhoc", Line: 1, StmtStart: intPtr(342), StmtEnd: intPtr(702), Text: "unknown",
								SQLHandle: "0x0200000029b796270f41bcfffe57737cdfd60af55cd966c90000000000000000000000000000000000000000"},
							{ProcName: "unknown", Line: 1, SQLHandle: zeroHandle, Text: "unknown"},
						},
						InputBuffer: writerBatch, Statement: writerStatement, StatementLength: 181,
						WaitingSince: timePtr(waitedFrom(384)),
					},
				},
				Resources: []deadlocks.Resource{
					{
						Kind: "keylock", LockID: "lock2785d2bdf00", Mode: "X", DatabaseID: 8, ObjectName: uatActivity,
						Object: "AsActivity", Index: "CX_ASACTIVITY_EFFDATE_GUID", IndexType: deadlocks.IndexClustered, HobtID: cxHobt,
						Resolution: deadlocks.ResolutionNamed,
						Owners: []deadlocks.LockRequest{
							{ProcessID: "process27c135908c8", Mode: "S", RequestType: "wait"},
							{ProcessID: "process27c0b7528c8", Mode: "X"},
						},
						Waiters: []deadlocks.LockRequest{
							{ProcessID: "process2784a65e108", Mode: "S", RequestType: "wait"},
							{ProcessID: "process275d4c63848", Mode: "S", RequestType: "wait"},
							{ProcessID: "process27c135908c8", Mode: "S", RequestType: "wait"},
						},
						Attrs: clusteredAttrs,
					},
					{
						Kind: "keylock", LockID: "lock26f6b5c7400", Mode: "U", DatabaseID: 8, ObjectName: uatActivity,
						Object: "AsActivity", Index: "IX_2_AsActivity", IndexType: deadlocks.IndexNonclustered, HobtID: ix2Hobt,
						Resolution: deadlocks.ResolutionNamed,
						Owners: []deadlocks.LockRequest{
							{ProcessID: "process27c135908c8", Mode: "S"},
							{ProcessID: "process275d4c63848", Mode: "S"},
						},
						Waiters: []deadlocks.LockRequest{{ProcessID: "process27c0b7528c8", Mode: "X", RequestType: "convert"}},
						Attrs: map[string]string{
							"hobtid": "72057595309719552", "dbid": "8", "objectname": uatActivity, "indexname": "IX_2_AsActivity",
							"id": "lock26f6b5c7400", "mode": "U", "associatedObjectId": "72057595309719552",
						},
					},
				},
			},
			XML: strings.TrimSpace(report),
		}))
	})
})
