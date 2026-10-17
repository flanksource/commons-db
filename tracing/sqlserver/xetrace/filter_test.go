package xetrace

import (
	"reflect"
	"testing"
	"time"

	"github.com/flanksource/commons-db/query/sqltext"
)

// ev builds an event with the derived fields populated as toEvent would.
func ev(sql string) Event {
	return Event{SQL: sql, StatementType: sqltext.ClassifyStatement(sql), Tables: sqltext.ExtractTables(sql)}
}

func TestEventFilterMatch(t *testing.T) {
	sel := ev("SELECT * FROM AsActivity")
	upd := ev("UPDATE AsActivity SET StatusCode = '09'")
	ins := ev("INSERT INTO AsCode (id) VALUES (1)")
	join := ev("SELECT * FROM AsActivity a, AsClient c WHERE a.id = c.id")
	noTable := ev("SET NOCOUNT ON")
	proc := ev("EXEC usp_GetOrderTotals '9F1C', '2026-08-30', 100000")
	health := Event{Name: "scheduler_monitor_non_yielding_ring_buffer_recorded", DatabaseName: "warehouse", Username: "svc", ClientApp: "orders-api", ClientHost: "app-0"}

	cases := []struct {
		name   string
		filter EventFilter
		in     []Event
		want   []Event
	}{
		{"zero filter passes all", EventFilter{}, []Event{sel, upd, noTable}, []Event{sel, upd, noTable}},
		{"dml group matches writes", EventFilter{Types: []string{"DML"}}, []Event{sel, upd, ins}, []Event{upd, ins}},
		{"exclude select", EventFilter{Types: []string{"!SELECT"}}, []Event{sel, upd}, []Event{upd}},
		{"comma list of types", EventFilter{Types: []string{"SELECT,INSERT"}}, []Event{sel, upd, ins}, []Event{sel, ins}},
		{"table exact", EventFilter{Tables: []string{"AsActivity"}}, []Event{sel, ins}, []Event{sel}},
		{"table wildcard", EventFilter{Tables: []string{"As*"}}, []Event{sel, ins}, []Event{sel, ins}},
		{"positive table drops table-less", EventFilter{Tables: []string{"AsActivity"}}, []Event{sel, noTable}, []Event{sel}},
		{"exclusion-only table keeps table-less", EventFilter{Tables: []string{"!AsCode"}}, []Event{sel, ins, noTable}, []Event{sel, noTable}},
		{"type and table combined", EventFilter{Types: []string{"DML"}, Tables: []string{"AsActivity"}}, []Event{sel, upd, ins}, []Event{upd}},
		// A multi-table join must be matched on EITHER referenced table for
		// include and dropped when ANY referenced table is excluded — the
		// behaviour that depends on ExtractTables finding both names.
		{"include matches a joined table", EventFilter{Tables: []string{"AsClient"}}, []Event{join, sel}, []Event{join}},
		{"exclude drops a join touching the excluded table", EventFilter{Tables: []string{"!AsClient"}}, []Event{join, sel}, []Event{sel}},
		// A stored-procedure call must be reachable by name and by type;
		// before EXEC was classified it carried no tokens and every positive
		// filter silently dropped it.
		{"proc matched by name", EventFilter{Tables: []string{"usp_GetOrderTotals"}}, []Event{proc, sel}, []Event{proc}},
		{"proc matched by wildcard", EventFilter{Tables: []string{"usp_*"}}, []Event{proc, sel}, []Event{proc}},
		{"proc excluded by wildcard", EventFilter{Tables: []string{"!usp_*"}}, []Event{proc, sel}, []Event{sel}},
		{"proc matched by type", EventFilter{Types: []string{"EXEC"}}, []Event{proc, sel, upd}, []Event{proc}},
		{"exec is not DML", EventFilter{Types: []string{"DML"}}, []Event{proc, upd}, []Event{upd}},
		{"event name", EventFilter{Events: []string{"scheduler_monitor*"}}, []Event{health, sel}, []Event{health}},
		{"database user app and host", EventFilter{Databases: []string{"warehouse"}, Users: []string{"svc"}, Apps: []string{"orders-*"}, Hosts: []string{"app-*"}}, []Event{health, sel}, []Event{health}},
		{"positive metadata drops missing values", EventFilter{Users: []string{"svc"}}, []Event{health, sel}, []Event{health}},
		{"exclusion metadata keeps missing values", EventFilter{Users: []string{"!sa"}}, []Event{health, sel}, []Event{health, sel}},
		{"minimum duration keeps duration-less diagnostics", EventFilter{MinDuration: time.Millisecond}, []Event{health, {Duration: 500 * time.Microsecond}, {Duration: 2 * time.Millisecond}}, []Event{health, {Duration: 2 * time.Millisecond}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []Event
			for _, e := range tc.in {
				if tc.filter.match(e) {
					got = append(got, e)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("matched %v, want %v", sqls(got), sqls(tc.want))
			}
		})
	}
}

// TestFilterableTypesCoversEveryToken guards the option set a UI offers against
// the tokens the filter actually matches: every StatementType in StatementTypes,
// plus the virtual DML group, must be offerable, and nothing that
// is not a real token may be offered. Adding a StatementType without extending
// FilterableTypes fails here rather than silently hiding a filter from the form.
func TestFilterableTypesCoversEveryToken(t *testing.T) {
	offered := map[string]struct{}{}
	for _, name := range FilterableTypes {
		offered[name] = struct{}{}
	}

	want := map[string]struct{}{dmlGroup: {}}
	for _, typ := range sqltext.StatementTypes {
		want[string(typ)] = struct{}{}
	}

	for name := range want {
		if _, ok := offered[name]; !ok {
			t.Errorf("FilterableTypes is missing %q", name)
		}
	}
	for name := range offered {
		if _, ok := want[name]; !ok {
			t.Errorf("FilterableTypes offers %q, which no event ever carries", name)
		}
	}
	if len(FilterableTypes) != len(offered) {
		t.Errorf("FilterableTypes has duplicates: %v", FilterableTypes)
	}
}

func sqls(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.SQL
	}
	return out
}
