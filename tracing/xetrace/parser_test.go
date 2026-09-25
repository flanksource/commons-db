package xetrace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseRingBuffer_Fixture(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "ring_buffer_sample.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	got, err := ParseRingBuffer(string(payload))
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}

	ts1, _ := time.Parse(time.RFC3339Nano, "2026-04-15T10:23:45.123Z")
	ts2, _ := time.Parse(time.RFC3339Nano, "2026-04-15T10:23:46.500Z")
	want := []Event{
		{
			Name:          "sql_statement_completed",
			Timestamp:     ts1,
			Duration:      12345 * time.Microsecond,
			CPUTime:       8000 * time.Microsecond,
			LogicalReads:  42,
			RowCount:      1,
			DatabaseName:  "warehouse",
			ClientApp:     "reporting",
			ClientHost:    "app-0",
			Username:      "analytics",
			SessionID:     73,
			Statement:     "SELECT COUNT(*) FROM AsActivity",
			SQL:           "SELECT COUNT(*) FROM AsActivity",
			StatementType: StmtSelect,
			Tables:        []string{"AsActivity"},
		},
		{
			Name:          "error_reported",
			Timestamp:     ts2,
			DatabaseName:  "warehouse",
			Username:      "analytics",
			SessionID:     73,
			StatementType: StmtOther,
			ErrorNumber:   208,
			ErrorMessage:  "Invalid object name 'NoSuchTable'.",
		},
	}

	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("ParseRingBuffer mismatch\n got: %#v\nwant: %#v", got.Events, want)
	}

	// The root's bookkeeping is what makes a skipped poll auditable: without
	// TotalEventsProcessed there is no way to tell that the ring buffer evicted
	// events between two successful reads.
	wantStats := RingBufferStats{TotalEventsProcessed: 2, EventCount: 2, MemoryUsed: 320}
	if got.Stats != wantStats {
		t.Errorf("Stats = %#v, want %#v", got.Stats, wantStats)
	}
}

// TestParseRingBuffer_StatsReportTruncationAndDrops pins the two failure
// classes the stats exist to surface. Both are silent today: a truncated
// target_data yields a short (or unparseable) document, and droppedCount is the
// server telling us it refused to buffer events at all.
func TestParseRingBuffer_StatsReportTruncationAndDrops(t *testing.T) {
	got, err := ParseRingBuffer(`<RingBufferTarget truncated="1" processingTime="17" ` +
		`totalEventsProcessed="4096" eventCount="1000" droppedCount="96" memoryUsed="4194304"/>`)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	want := RingBufferStats{
		Truncated:            true,
		ProcessingTime:       17,
		TotalEventsProcessed: 4096,
		EventCount:           1000,
		DroppedCount:         96,
		MemoryUsed:           4194304,
	}
	if got.Stats != want {
		t.Errorf("Stats = %#v, want %#v", got.Stats, want)
	}
}

// TestParseRingBuffer_SPStatementCausality covers the fields that only appear
// once sp_statement_completed is opted into: the owning object (by name or, on
// server versions that report only the id, by object_id) and the
// attach_activity_id that Nest groups on. The activity id is a GUID followed by
// a sequence number, and the GUID contains hyphens of its own, so the sequence
// must be split off the LAST one.
func TestParseRingBuffer_SPStatementCausality(t *testing.T) {
	payload := `<RingBufferTarget truncated="0" eventCount="1">
  <event name="sp_statement_completed" package="sqlserver" timestamp="2026-04-15T10:23:45.123Z">
    <data name="duration"><value>409000</value></data>
    <data name="logical_reads"><value>88320</value></data>
    <data name="object_name"><value>usp_GetOrderTotals</value></data>
    <data name="object_id"><value>1445580188</value></data>
    <data name="statement"><value>SELECT AsDepositValue.fundGuid FROM AsDepositValue</value></data>
    <action name="attach_activity_id" package="package0"><value>7A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9-17</value></action>
    <action name="session_id" package="sqlserver"><value>73</value></action>
  </event>
</RingBufferTarget>`

	got, err := ParseRingBuffer(payload)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got.Events))
	}
	e := got.Events[0]
	if e.ObjectName != "usp_GetOrderTotals" {
		t.Errorf("ObjectName = %q, want usp_GetOrderTotals", e.ObjectName)
	}
	if e.ObjectID != 1445580188 {
		t.Errorf("ObjectID = %d, want 1445580188", e.ObjectID)
	}
	if e.ActivityID != "7A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9" {
		t.Errorf("ActivityID = %q — the GUID's own hyphens must be preserved", e.ActivityID)
	}
	if e.ActivitySeq != 17 {
		t.Errorf("ActivitySeq = %d, want 17", e.ActivitySeq)
	}
	if e.Duration != 409*time.Millisecond {
		t.Errorf("Duration = %s, want 409ms", e.Duration)
	}
}

func TestParseActivityID(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantID  string
		wantSeq int
	}{
		{"guid with sequence", "7A1B-2C3D-4", "7A1B-2C3D", 4},
		{"no sequence separator", "7A1B2C3D", "7A1B2C3D", 0},
		{"unparseable sequence keeps the id", "7A1B-2C3D-x", "7A1B-2C3D-x", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, seq := parseActivityID(tc.in)
			if id != tc.wantID || seq != tc.wantSeq {
				t.Errorf("parseActivityID(%q) = %q, %d; want %q, %d", tc.in, id, seq, tc.wantID, tc.wantSeq)
			}
		})
	}
}

func TestParseRingBuffer_Empty(t *testing.T) {
	got, err := ParseRingBuffer(`<RingBufferTarget truncated="0" eventCount="0"/>`)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	if len(got.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(got.Events))
	}
	if got.Stats.EventCount != 0 || got.Stats.Truncated {
		t.Errorf("Stats = %#v, want a zero-event untruncated target", got.Stats)
	}
}

func TestParseRingBuffer_InvalidXML(t *testing.T) {
	_, err := ParseRingBuffer("not xml <<< broken")
	if err == nil {
		t.Fatal("expected error for invalid xml")
	}
}

func TestEvent_Key_IsStable(t *testing.T) {
	ts, _ := time.Parse(time.RFC3339Nano, "2026-04-15T10:23:45.123Z")
	a := Event{Timestamp: ts, SessionID: 73, Duration: 12345 * time.Microsecond, Statement: "SELECT 1"}
	b := Event{Timestamp: ts, SessionID: 73, Duration: 12345 * time.Microsecond, Statement: "SELECT 1"}
	if a.Key() != b.Key() {
		t.Errorf("Key should be stable for identical events: %q vs %q", a.Key(), b.Key())
	}

	c := Event{Timestamp: ts, SessionID: 74, Duration: 12345 * time.Microsecond, Statement: "SELECT 1"}
	if a.Key() == c.Key() {
		t.Errorf("Key should differ when session_id differs")
	}
}

func TestIsNoiseStatement(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"whitespace only", "   \n\t  ", false},
		{"plain select 1", "SELECT 1", true},
		{"lowercase select 1", "select 1", true},
		{"padded select 1", "  SELECT   1  ", true},
		{"select 1 with semicolon", "SELECT 1;", true},
		{"select 1 from dual survives", "SELECT 1 FROM dual", false},
		{"select 1 where clause survives", "SELECT 1 WHERE 1=1", false},
		{"trancount bare", "IF @@TRANCOUNT > 0", true},
		{"trancount extra spaces", "if  @@TRANCOUNT >  0", true},
		{"trancount commit", "IF @@TRANCOUNT > 0 COMMIT TRAN", true},
		{"trancount commit lowercase", "if @@trancount > 0 commit tran", true},
		{"trancount rollback is not in set", "IF @@TRANCOUNT > 0 ROLLBACK TRAN", false},
		{"bare commit tran", "COMMIT TRAN", true},
		{"bare commit tran lowercase", "commit tran", true},
		{"commit tran with semicolon", "COMMIT TRAN;", true},
		{"named commit transaction survives", "COMMIT TRANSACTION MyTx", false},
		{"exec sp_unprepare with handle", "exec sp_unprepare 7", true},
		{"exec sp_unprepare uppercase", "EXEC sp_unprepare 42", true},
		{"exec sp_unprepare many spaces", "EXEC   sp_unprepare   9", true},
		{"sp_unprepare without exec survives", "sp_unprepare 7", false},
		{"sp_unpreparexyz does not match prefix", "EXEC sp_unpreparexyz 7", false},
		{"real user query", "SELECT * FROM AsUser WHERE ClientNumber = 'alice'", false},
		{"set quoted_identifier off", "SET QUOTED_IDENTIFIER OFF", true},
		{"set quoted_identifier on lowercase", "set quoted_identifier on", true},
		{"set textsize 4096", "SET TEXTSIZE 4096", true},
		{"set textsize with semicolon", "SET TEXTSIZE 4096;", true},
		{"set arithabort on", "SET ARITHABORT ON", true},
		{"set ansi_nulls off", "SET ANSI_NULLS OFF", true},
		{"set transaction isolation level", "SET TRANSACTION ISOLATION LEVEL READ COMMITTED", true},
		{"set nocount on", "SET NOCOUNT ON", true},
		{"real SET variable assignment survives", "SET @x = 1", false},
		{"update on SET column survives", "UPDATE t SET QUOTED_IDENTIFIER = 'x'", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNoiseStatement(c.in); got != c.want {
				t.Errorf("isNoiseStatement(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseRingBuffer_DropsNoiseStatements(t *testing.T) {
	payload := `<RingBufferTarget truncated="0" eventCount="4">
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:00.000Z">
    <data name="duration"><value>100</value></data>
    <data name="statement"><value>SELECT 1</value></data>
    <action name="session_id" package="sqlserver"><value>10</value></action>
  </event>
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:00.100Z">
    <data name="duration"><value>100</value></data>
    <data name="statement"><value>IF @@TRANCOUNT &gt; 0</value></data>
    <action name="session_id" package="sqlserver"><value>10</value></action>
  </event>
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:00.200Z">
    <data name="duration"><value>100</value></data>
    <data name="statement"><value>IF @@TRANCOUNT &gt; 0 COMMIT TRAN</value></data>
    <action name="session_id" package="sqlserver"><value>10</value></action>
  </event>
  <event name="sql_statement_completed" package="sqlserver" timestamp="2026-04-15T10:00:00.300Z">
    <data name="duration"><value>9999</value></data>
    <data name="statement"><value>SELECT 42 FROM AsUser</value></data>
    <action name="session_id" package="sqlserver"><value>10</value></action>
  </event>
</RingBufferTarget>`

	got, err := ParseRingBuffer(payload)
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("expected 1 surviving event, got %d: %+v", len(got.Events), got.Events)
	}
	if got.Events[0].SQL != "SELECT 42 FROM AsUser" {
		t.Errorf("unexpected surviving statement: %q", got.Events[0].SQL)
	}
}
