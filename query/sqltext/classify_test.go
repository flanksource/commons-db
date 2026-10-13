package sqltext

import (
	"reflect"
	"testing"
)

// TestStatementTypesCoversClassifier guards StatementTypes against the verbs
// ClassifyStatement maps: adding a verb's type without listing it fails here.
func TestStatementTypesCoversClassifier(t *testing.T) {
	listed := map[StatementType]struct{}{}
	for _, typ := range StatementTypes {
		listed[typ] = struct{}{}
	}
	for verb, typ := range classKeywords {
		if _, ok := listed[typ]; !ok {
			t.Errorf("StatementTypes is missing %q, which ClassifyStatement returns for %s", typ, verb)
		}
	}
	for _, typ := range []StatementType{StmtDDL, StmtOther} {
		if _, ok := listed[typ]; !ok {
			t.Errorf("StatementTypes is missing %q", typ)
		}
	}
}

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
		{"unwrapped procedure call", "EXEC usp_GetOrderTotals '9F1C', '2026-08-30', '2026-08-30', 100000", StmtExec},
		{"select wins over a later exec", "SELECT * FROM AsActivity WHERE x = 1 -- EXEC later\n", StmtSelect},
		{"create table is ddl", "CREATE TABLE Foo (id int)", StmtDDL},
		{"alter is ddl", "ALTER INDEX IX_15_ASACTIVITY ON AsActivity REBUILD", StmtDDL},
		{"drop is ddl", "drop table #t", StmtDDL},
		{"truncate is ddl", "TRUNCATE TABLE AsStaging", StmtDDL},
		{"a procedure body does not make its create a select", "CREATE PROCEDURE p AS SELECT 1", StmtDDL},
		{"create after a comment is ddl", "/* rebuild */ -- step 1\nCREATE INDEX IX ON AsActivity (StatusCode)", StmtDDL},
		{"select into a temp table stays select", "SELECT id INTO #t FROM AsActivity", StmtSelect},
		{"dynamic ddl stays the exec that ran it", "EXEC sp_executesql N'CREATE TABLE Foo (id int)'", StmtExec},
		{"set is other", "SET NOCOUNT ON", StmtOther},
		{"keyword inside string not matched first", "SELECT 'DELETE ME' AS note", StmtSelect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyStatement(tc.sql); got != tc.want {
				t.Errorf("ClassifyStatement(%q) = %q, want %q", tc.sql, got, tc.want)
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
		{"exec names the procedure", "EXEC usp_GetOrderTotals '9F1C', '2026-08-30', 100000", []string{"usp_GetOrderTotals"}},
		{"execute spelled out", "EXECUTE dbo.usp_GetFundValues 1", []string{"usp_GetFundValues"}},
		{"exec args are not a table list", "EXEC p 1, 2", []string{"p"}},
		// A schema change names the table it changes, so a DDL row is not
		// table-less and a --table filter keeps it.
		{"create table", "CREATE TABLE dbo.AsAudit (id int)", []string{"AsAudit"}},
		{"alter table", "ALTER TABLE [dbo].[AsPolicy] ADD Note varchar(10)", []string{"AsPolicy"}},
		{"drop table after a guard", "IF OBJECT_ID('dbo.AsTmp') IS NOT NULL DROP TABLE dbo.AsTmp", []string{"AsTmp"}},
		{"truncate table", "TRUNCATE TABLE AsStaging", []string{"AsStaging"}},
		{"a table variable names no table", "DECLARE @t TABLE (id int)", nil},
		{"an inline function returns no table named AS", "CREATE FUNCTION f() RETURNS TABLE AS RETURN (SELECT id FROM AsCode)", []string{"AsCode"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractTables(tc.sql); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractTables(%q) = %#v, want %#v", tc.sql, got, tc.want)
			}
		})
	}
}
