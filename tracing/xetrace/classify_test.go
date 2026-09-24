package xetrace

import (
	"reflect"
	"testing"
)

func TestClassifyStatement(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want StatementType
	}{
		{"plain select", "SELECT ActivityGUID FROM AsActivity", StmtSelect},
		{"lowercase", "select 1 from AsCode", StmtSelect},
		{"leading line comment", "-- pick one\nSELECT * FROM AsClient", StmtSelect},
		{"leading block comment", "/* hint */ UPDATE AsActivity SET StatusCode = '09'", StmtUpdate},
		{"cte resolves to outer select", "WITH c AS (SELECT 1 x) SELECT x FROM c", StmtSelect},
		{"insert into select is insert", "INSERT INTO AsAudit (id) SELECT id FROM AsActivity", StmtInsert},
		{"update with subquery", "UPDATE AsActivity SET v = (SELECT MAX(v) FROM AsCode)", StmtUpdate},
		{"delete from", "DELETE FROM AsActivity WHERE StatusCode = '99'", StmtDelete},
		{"merge", "MERGE INTO AsActivity AS t USING src ON t.id = src.id", StmtMerge},
		{"exec", "EXEC sp_who2", StmtExec},
		{"execute spelled out", "EXECUTE sp_who2", StmtExec},
		{"unwrapped oipa proc call", "EXEC asc_GetDepositValueList '9F1C', '2026-08-30', '2026-08-30', 100000", StmtExec},
		{"select wins over a later exec", "SELECT * FROM AsActivity WHERE x = 1 -- EXEC later\n", StmtSelect},
		{"ddl create is other", "CREATE TABLE Foo (id int)", StmtOther},
		{"set is other", "SET NOCOUNT ON", StmtOther},
		{"keyword inside string not matched first", "SELECT 'DELETE ME' AS note", StmtSelect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStatement(tc.sql); got != tc.want {
				t.Errorf("classifyStatement(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

func TestExtractTables(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"single from", "SELECT * FROM AsActivity", []string{"AsActivity"}},
		{"joins", "SELECT * FROM AsActivity a JOIN AsClient c ON a.id = c.id", []string{"AsActivity", "AsClient"}},
		{"insert into", "INSERT INTO AsAudit (id) VALUES (1)", []string{"AsAudit"}},
		{"update target", "UPDATE AsActivity SET StatusCode = '09'", []string{"AsActivity"}},
		{"delete from", "DELETE FROM AsActivity WHERE id = 1", []string{"AsActivity"}},
		{"schema qualified", "SELECT * FROM dbo.AsActivity", []string{"AsActivity"}},
		{"bracketed qualified", "SELECT * FROM [dbo].[AsActivity]", []string{"AsActivity"}},
		{"comma list", "SELECT * FROM AsActivity, AsClient", []string{"AsActivity", "AsClient"}},
		{"comma list with aliases", "SELECT * FROM AsActivity a, AsClient c WHERE a.id = c.id", []string{"AsActivity", "AsClient"}},
		{"three table comma with aliases", "SELECT * FROM AsActivity a, AsClient c, AsCode d", []string{"AsActivity", "AsClient", "AsCode"}},
		{"ansi join with aliases", "SELECT * FROM AsActivity a INNER JOIN AsClient c ON a.id = c.id", []string{"AsActivity", "AsClient"}},
		{"nolock table hint", "SELECT * FROM AsActivity a WITH (NOLOCK) JOIN AsClient c WITH (NOLOCK) ON a.id = c.id", []string{"AsActivity", "AsClient"}},
		{"merge using source, no SET phantom", "MERGE INTO AsActivity AS t USING AsStaging AS s ON t.id = s.id WHEN MATCHED THEN UPDATE SET t.x = s.x", []string{"AsActivity", "AsStaging"}},
		{"subquery skipped, inner scanned", "SELECT * FROM (SELECT id FROM AsCode) t", []string{"AsCode"}},
		{"subquery column commas do not leak, outer list continues", "SELECT * FROM (SELECT a, b FROM AsCode) x, AsClient c", []string{"AsCode", "AsClient"}},
		{"dedup case insensitive", "SELECT * FROM AsActivity a JOIN asactivity b ON a.id = b.id", []string{"AsActivity"}},
		{"no tables", "SET NOCOUNT ON", nil},
		// A stored-procedure call names the procedure, not the tables its body
		// reads — the body is not in the trace text. Without this the whole
		// event carries no tokens and any positive --table filter drops it.
		{"exec names the procedure", "EXEC asc_GetDepositValueList '9F1C', '2026-08-30', 100000", []string{"asc_GetDepositValueList"}},
		{"execute spelled out", "EXECUTE dbo.asc_GetFundValueList 1", []string{"asc_GetFundValueList"}},
		{"exec args are not a table list", "EXEC p 1, 2", []string{"p"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTables(tc.sql); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractTables(%q) = %#v, want %#v", tc.sql, got, tc.want)
			}
		})
	}
}

// ev builds an event with the derived fields populated as toEvent would.
func ev(sql string) Event {
	return Event{SQL: sql, StatementType: classifyStatement(sql), Tables: extractTables(sql)}
}

func TestEventFilterApply(t *testing.T) {
	sel := ev("SELECT * FROM AsActivity")
	upd := ev("UPDATE AsActivity SET StatusCode = '09'")
	ins := ev("INSERT INTO AsCode (id) VALUES (1)")
	join := ev("SELECT * FROM AsActivity a, AsClient c WHERE a.id = c.id")
	noTable := ev("SET NOCOUNT ON")
	proc := ev("EXEC asc_GetDepositValueList '9F1C', '2026-08-30', 100000")

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
		// behaviour that depends on extractTables finding both names.
		{"include matches a joined table", EventFilter{Tables: []string{"AsClient"}}, []Event{join, sel}, []Event{join}},
		{"exclude drops a join touching the excluded table", EventFilter{Tables: []string{"!AsClient"}}, []Event{join, sel}, []Event{sel}},
		// A stored-procedure call must be reachable by name and by type;
		// before EXEC was classified it carried no tokens and every positive
		// filter silently dropped it.
		{"proc matched by name", EventFilter{Tables: []string{"asc_GetDepositValueList"}}, []Event{proc, sel}, []Event{proc}},
		{"proc matched by wildcard", EventFilter{Tables: []string{"asc_*"}}, []Event{proc, sel}, []Event{proc}},
		{"proc excluded by wildcard", EventFilter{Tables: []string{"!asc_*"}}, []Event{proc, sel}, []Event{sel}},
		{"proc matched by type", EventFilter{Types: []string{"EXEC"}}, []Event{proc, sel, upd}, []Event{proc}},
		{"exec is not DML", EventFilter{Types: []string{"DML"}}, []Event{proc, upd}, []Event{upd}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.filter.Apply(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Apply() = %v, want %v", sqls(got), sqls(tc.want))
			}
		})
	}
}

// TestFilterableTypesCoversEveryToken guards the option set a UI offers against
// the tokens the filter actually matches: every StatementType classifyStatement
// can produce, plus the virtual DML group, must be offerable, and nothing that
// is not a real token may be offered. Adding a StatementType without extending
// FilterableTypes fails here rather than silently hiding a filter from the form.
func TestFilterableTypesCoversEveryToken(t *testing.T) {
	offered := map[string]struct{}{}
	for _, name := range FilterableTypes {
		offered[name] = struct{}{}
	}

	want := map[string]struct{}{string(StmtOther): {}, dmlGroup: {}}
	for _, typ := range classKeywords {
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
