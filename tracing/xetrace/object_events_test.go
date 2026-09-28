package xetrace

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Each object event keeps only a committed change a user made: the commit
// phase (a DDL statement raises Begin then Commit or Rollback), outside tempdb
// (#temp tables), and not the statistics SQL Server creates on its own. Those
// intrinsic tests lead; the session filters follow; and there is no duration
// to test, so --min-duration never drops a schema change.
func TestBuildCreateSQL_ObjectEvents(t *testing.T) {
	got, err := BuildCreateSQL(CreateOptions{
		Name: "s", Events: ObjectEvents, MinDurationMicros: 1000, Hosts: []string{"cycle*"},
		MaxMemoryKB: 1024, MaxEvents: 100,
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	for _, name := range ObjectEvents {
		clause := extractEventClause(t, got, name)
		if head := "ADD EVENT sqlserver." + name + " (\n    SET collect_database_name = (1)\n    ACTION ("; !strings.HasPrefix(clause, head) {
			t.Errorf("%s must collect its database name before its actions:\n%s", name, clause)
		}
		if want := "WHERE (ddl_phase = 1 AND database_id <> 2 AND object_type <> 21587 AND "; !strings.Contains(clause, want) {
			t.Errorf("%s must lead with its intrinsic predicates %q:\n%s", name, want, clause)
		}
		if want := "sqlserver.like_i_sql_unicode_string(sqlserver.client_hostname, N'cycle%')"; !strings.Contains(clause, want) {
			t.Errorf("%s must carry the session filters:\n%s", name, clause)
		}
		if strings.Contains(clause, "duration") {
			t.Errorf("%s has no duration field to predicate on:\n%s", name, clause)
		}
	}
}

func TestObjectEventsAreOptIn(t *testing.T) {
	for _, name := range ObjectEvents {
		if slices.Contains(DefaultEvents, name) {
			t.Errorf("%s must not be in DefaultEvents — --event opts in", name)
		}
		if !slices.Contains(SupportedEvents, name) {
			t.Errorf("%s must be in SupportedEvents so NormalizeEvents accepts it", name)
		}
	}
}

func TestParseRingBuffer_ObjectEvents(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "ring_buffer_object_events.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := ParseRingBuffer(string(payload))
	if err != nil {
		t.Fatalf("ParseRingBuffer: %v", err)
	}
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	want := []Event{
		{
			// The object's database is the event's own field, not the session's
			// current database the action reports.
			Name: EventObjectCreated, Timestamp: at("2026-09-18T10:00:00.100Z"),
			DatabaseName: "warehouse", ClientApp: "sqlcmd", ClientHost: "dba-laptop", Username: "dba", SessionID: 61,
			Statement: "CREATE TABLE warehouse.dbo.AsAudit (id int)", SQL: "CREATE TABLE warehouse.dbo.AsAudit (id int)",
			StatementType: StmtDDL, Tables: []string{"AsAudit"},
			ObjectName: "AsAudit", ObjectID: 1205579333, ObjectType: "USRTAB", Sequence: 11,
			AdditionalFields: map[string]any{"database_id": "7", "ddl_phase": "1", "index_id": "0", "related_object_id": "0", "transaction_id": "9012"},
		},
		{
			Name: EventObjectAltered, Timestamp: at("2026-09-18T10:00:01.200Z"), DatabaseName: "warehouse", SessionID: 61,
			Statement: "ALTER INDEX IX_AsPolicy_Status ON dbo.AsPolicy REBUILD", SQL: "ALTER INDEX IX_AsPolicy_Status ON dbo.AsPolicy REBUILD",
			StatementType: StmtDDL,
			ObjectName:    "IX_AsPolicy_Status", ObjectID: 581577110, ObjectType: "INDEX", Sequence: 12,
			AdditionalFields: map[string]any{"database_id": "7", "ddl_phase": "1", "index_id": "3", "related_object_id": "0", "transaction_id": "9013"},
		},
		{
			// The batch opens with IF, which classifies as OTHER; the event itself
			// says a schema change committed.
			Name: EventObjectDeleted, Timestamp: at("2026-09-18T10:00:02.300Z"), DatabaseName: "warehouse", SessionID: 61,
			Statement:     "IF OBJECT_ID('dbo.AsAudit') IS NOT NULL DROP TABLE dbo.AsAudit",
			SQL:           "IF OBJECT_ID('dbo.AsAudit') IS NOT NULL DROP TABLE dbo.AsAudit",
			StatementType: StmtDDL, Tables: []string{"AsAudit"},
			ObjectName: "AsAudit", ObjectID: 1205579333, ObjectType: "USRTAB", Sequence: 13,
			AdditionalFields: map[string]any{"database_id": "7", "ddl_phase": "1"},
		},
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("events mismatch\n got: %#v\nwant: %#v", got.Events, want)
	}
}
