package xetrace

import (
	"reflect"
	"strings"
	"testing"
)

func rdsProbe() hostProbe {
	id := 5
	return hostProbe{HostPlatform: "Windows", ErrorLog: `D:\rdsdbdata\log\ERROR`, RDSAdminDBID: &id}
}

// TestEventFileDir covers every branch of the "where may this instance write
// .xel files" decision. The RDS case is the one a client-side guess always gets
// wrong: D:\rdsdbdata\log is the ONLY directory an RDS instance may write to.
func TestEventFileDir(t *testing.T) {
	cases := []struct {
		name     string
		probe    hostProbe
		override string
		want     string
		wantSep  string
		error    string
	}{
		{
			name:    "RDS pins the one writable directory",
			probe:   rdsProbe(),
			want:    `D:\rdsdbdata\log`,
			wantSep: `\`,
		},
		{
			name:    "linux uses the error log's directory",
			probe:   hostProbe{HostPlatform: "Linux", ErrorLog: "/var/opt/mssql/log/errorlog"},
			want:    "/var/opt/mssql/log",
			wantSep: "/",
		},
		{
			name:    "windows uses the error log's directory",
			probe:   hostProbe{HostPlatform: "Windows", ErrorLog: `C:\Program Files\Microsoft SQL Server\MSSQL16.MSSQLSERVER\MSSQL\Log\ERRORLOG`},
			want:    `C:\Program Files\Microsoft SQL Server\MSSQL16.MSSQLSERVER\MSSQL\Log`,
			wantSep: `\`,
		},
		{
			name:     "an operator override wins over the probe",
			probe:    rdsProbe(),
			override: `E:\xe\`,
			want:     `E:\xe`,
			wantSep:  `\`,
		},
		{
			name:  "an unusable probe is an error, not a guess",
			probe: hostProbe{HostPlatform: "Linux", ErrorLog: ""},
			error: "sqltrace.eventFile.dir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := eventFileDir(tc.probe, tc.override)
			if tc.error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("eventFileDir() error = %v, want containing %q", err, tc.error)
				}
				return
			}
			if err != nil {
				t.Fatalf("eventFileDir(): %v", err)
			}
			if got != tc.want {
				t.Errorf("eventFileDir() = %q, want %q", got, tc.want)
			}
			if sep := tc.probe.separator(); sep != tc.wantSep {
				t.Errorf("separator() = %q, want %q", sep, tc.wantSep)
			}
		})
	}
}

func TestValidateEventFilePath(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		error string
	}{
		{name: "auto is resolved later, not rejected now", path: AutoPath},
		{name: "AUTO is case-insensitive", path: "AUTO"},
		{name: "a linux path", path: "/var/opt/mssql/log/x.xel"},
		{name: "an RDS path", path: `D:\rdsdbdata\log\x.xel`},
		{name: "a UNC path", path: `\\host\share\x.xel`},
		{name: "a relative path", path: "xe/x.xel", error: "must be absolute"},
		{name: "the wrong extension", path: "/var/opt/mssql/log/x.log", error: "must end in .xel"},
		{name: "a wildcard collides with the read glob", path: "/var/opt/mssql/log/x*.xel", error: "must not contain a wildcard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEventFilePath(tc.path)
			if tc.error == "" {
				if err != nil {
					t.Fatalf("ValidateEventFilePath(%q): %v", tc.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.error) {
				t.Fatalf("ValidateEventFilePath(%q) error = %v, want containing %q", tc.path, err, tc.error)
			}
		})
	}
}

// SQL Server writes <base>_<n>_<ticks>.xel, so a read that used the configured
// name verbatim would find nothing at all.
func TestFileTargetReadPattern(t *testing.T) {
	target := FileTarget{Path: `D:\rdsdbdata\log\commons_db_trace_1.xel`}
	if got, want := target.readPattern(), `D:\rdsdbdata\log\commons_db_trace_1*.xel`; got != want {
		t.Errorf("readPattern() = %q, want %q", got, want)
	}
}

// The first poll must read from the start of the capture; every later one
// resumes at the last row it saw, which is what keeps a poll's cost
// proportional to the new events instead of the whole file.
func TestFileCursorReadArgs(t *testing.T) {
	const pattern = "/var/opt/mssql/log/x*.xel"

	first := fileCursor{}.readArgs(pattern)
	if len(first) != 3 || first[0] != pattern || first[1] != nil || first[2] != nil {
		t.Fatalf("first poll args = %#v, want [%q nil nil]", first, pattern)
	}

	resumed := fileCursor{file: "/var/opt/mssql/log/x_0_1337.xel", offset: 4096, valid: true}.readArgs(pattern)
	if len(resumed) != 3 || resumed[1] != "/var/opt/mssql/log/x_0_1337.xel" || resumed[2] != int64(4096) {
		t.Fatalf("resumed args = %#v, want the previous file and offset", resumed)
	}
}

// One event_file row carries a single <event> document rather than the ring
// buffer's whole payload, but it must decode to exactly the same Event.
func TestParseEventDataRow(t *testing.T) {
	const row = `<event name="sql_statement_completed" package="sqlserver" timestamp="2025-09-20T10:11:12.345Z">
  <data name="duration"><value>2500</value></data>
  <data name="logical_reads"><value>17</value></data>
  <data name="statement"><value>SELECT 1 FROM AsActivity</value></data>
  <action name="event_sequence" package="package0"><value>42</value></action>
  <action name="session_id" package="sqlserver"><value>57</value></action>
</event>`

	raw, err := parseEventDataRow(row)
	if err != nil {
		t.Fatalf("parseEventDataRow: %v", err)
	}
	events, excluded := collectEvents([]rawXMLEvent{raw})
	if len(events) != 1 || len(excluded) != 0 {
		t.Fatalf("collectEvents = %d events, %d excluded; want 1, 0", len(events), len(excluded))
	}
	e := events[0]
	if e.Name != EventSQLStatementCompleted || e.Sequence != 42 || e.SessionID != 57 {
		t.Errorf("identity fields wrong: %+v", e)
	}
	if e.Duration.Microseconds() != 2500 || e.LogicalReads != 17 {
		t.Errorf("cost fields wrong: duration=%s logical_reads=%d", e.Duration, e.LogicalReads)
	}
	if e.SQL != "SELECT 1 FROM AsActivity" {
		t.Errorf("SQL = %q", e.SQL)
	}
}

func TestParseEventDataRowPreservesAdditionalFieldsFlat(t *testing.T) {
	const row = `<event name="scheduler_monitor_non_yielding_ring_buffer_recorded" timestamp="2025-09-20T10:11:12.345Z">
  <data name="worker_count" type="uint32"><value>24</value></data>
  <data name="is_system" type="boolean"><value>true</value></data>
  <data name="wait_type" type="unicode_string"><text>RESOURCE_SEMAPHORE</text></data>
  <action name="database_name" type="unicode_string"><value>warehouse</value></action>
</event>`

	raw, err := parseEventDataRow(row)
	if err != nil {
		t.Fatalf("parseEventDataRow: %v", err)
	}
	events, _ := collectEvents([]rawXMLEvent{raw})
	if len(events) != 1 {
		t.Fatalf("collectEvents = %d events, want 1", len(events))
	}
	want := map[string]any{"worker_count": int64(24), "is_system": true, "wait_type": "RESOURCE_SEMAPHORE"}
	if !reflect.DeepEqual(events[0].AdditionalFields, want) {
		t.Fatalf("additional fields = %#v, want %#v", events[0].AdditionalFields, want)
	}
}

// Driver chatter must be dropped identically whichever target delivered it —
// and reported as observed, so the drop metric does not count our own filter.
func TestParseEventDataRowDropsNoiseAsExcluded(t *testing.T) {
	const row = `<event name="sql_statement_completed" package="sqlserver" timestamp="2025-09-20T10:11:12.345Z">
  <data name="statement"><value>SET NOCOUNT ON</value></data>
  <action name="event_sequence" package="package0"><value>7</value></action>
</event>`

	raw, err := parseEventDataRow(row)
	if err != nil {
		t.Fatalf("parseEventDataRow: %v", err)
	}
	events, excluded := collectEvents([]rawXMLEvent{raw})
	if len(events) != 0 || len(excluded) != 1 {
		t.Fatalf("collectEvents = %d events, %d excluded; want 0, 1", len(events), len(excluded))
	}
}

func TestParseEventDataRowRejectsMalformedXML(t *testing.T) {
	if _, err := parseEventDataRow("<event name=\"x\""); err == nil {
		t.Fatal("parseEventDataRow accepted malformed xml")
	}
}

func TestParseEventDataRowRejectsConflictingAdditionalFields(t *testing.T) {
	const row = `<event name="health"><data name="state"><value>one</value></data><action name="state"><value>two</value></action></event>`
	if _, err := parseEventDataRow(row); err == nil || !strings.Contains(err.Error(), `additional field "state" has conflicting values`) {
		t.Fatalf("parseEventDataRow error = %v, want conflicting additional field", err)
	}
}
