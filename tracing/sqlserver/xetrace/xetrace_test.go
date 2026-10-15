package xetrace

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeEvents(t *testing.T) {
	cases := []struct {
		name  string
		in    []string
		want  []string
		error string
	}{
		{name: "empty defers to DefaultEvents", in: nil},
		{name: "blank entries are dropped", in: []string{" ", ",", ""}},
		{
			name: "trims, lower-cases and de-duplicates",
			in:   []string{" RPC_Completed ", "rpc_completed"},
			want: []string{EventRPCCompleted},
		},
		{
			name: "flattens comma lists preserving order",
			in:   []string{"sql_statement_completed,error_reported", "rpc_completed"},
			want: []string{EventSQLStatementCompleted, EventErrorReported, EventRPCCompleted},
		},
		{
			name:  "rejects an unsupported event",
			in:    []string{"sql_statement_completed", " deadlock_report "},
			error: `unsupported event "deadlock_report"`,
		},
		{
			name:  "rejects an unsupported exclusion",
			in:    []string{"!deadlock_report"},
			error: `unsupported event "deadlock_report"`,
		},
		{
			name: "star selects every supported event",
			in:   []string{"*"},
			want: SupportedEvents,
		},
		{
			name: "a suffix pattern selects the supported events it matches",
			in:   []string{"*_completed"},
			want: []string{EventSQLStatementCompleted, EventRPCCompleted, EventSQLBatchCompleted, EventSPStatementCompleted},
		},
		{
			name: "a prefix pattern is case-insensitive",
			in:   []string{"SQL_*"},
			want: []string{EventSQLStatementCompleted, EventSQLBatchCompleted},
		},
		{
			name: "an exclusion-only list is the defaults minus the exclusions",
			in:   []string{"!error_reported"},
			want: []string{EventSQLStatementCompleted, EventRPCCompleted, EventSQLBatchCompleted},
		},
		{
			name: "an exclusion wins over a pattern",
			in:   []string{"*_completed,!sp_*"},
			want: []string{EventSQLStatementCompleted, EventRPCCompleted, EventSQLBatchCompleted},
		},
		{
			name: "a name and a pattern covering it are de-duplicated",
			in:   []string{"rpc_completed", "*_completed"},
			want: []string{EventRPCCompleted, EventSQLStatementCompleted, EventSQLBatchCompleted, EventSPStatementCompleted},
		},
		{
			name:  "a pattern matching nothing is an error",
			in:    []string{"deadlock*"},
			error: `selects no event`,
		},
		{
			name:  "excluding everything is an error",
			in:    []string{"rpc_completed", "!rpc_completed"},
			error: `selects no event`,
		},
		{
			name:  "a mid-pattern star is refused",
			in:    []string{"sql*completed"},
			error: `"sql*completed"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEvents(tc.in)
			if tc.error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("NormalizeEvents(%v) error = %v, want containing %q", tc.in, err, tc.error)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEvents(%v): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeEvents(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Every supported event must be accepted by NormalizeEvents and produce valid
// DDL, so the API/UI event picker can offer the whole set.
func TestNormalizeEventsAcceptsEverySupportedEvent(t *testing.T) {
	got, err := NormalizeEvents(SupportedEvents)
	if err != nil {
		t.Fatalf("NormalizeEvents(SupportedEvents): %v", err)
	}
	if !reflect.DeepEqual(got, SupportedEvents) {
		t.Errorf("NormalizeEvents(SupportedEvents) = %v, want %v", got, SupportedEvents)
	}
	ddl, err := BuildCreateSQL(CreateOptions{Name: "s", Events: got, MaxMemoryKB: 1024, MaxEvents: 100})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	for _, name := range SupportedEvents {
		if !strings.Contains(ddl, "ADD EVENT sqlserver."+name) {
			t.Errorf("ddl missing event %q:\n%s", name, ddl)
		}
	}
}
