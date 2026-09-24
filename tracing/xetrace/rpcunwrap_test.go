package xetrace

import (
	"strings"
	"testing"
)

// toEventFor builds an Event from a raw captured statement exactly as the
// parser would, so tests exercise the same derivation path as a live trace.
func toEventFor(raw string) Event {
	e := Event{Statement: raw}
	deriveFromStatement(&e)
	return e
}

func TestUnwrapRPC(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantOK bool
		want   string
	}{
		{
			name: "sp_prepexec single int param",
			in: `declare @p1 int set @p1=5089 exec sp_prepexec @p1 output,` +
				`N'@P0 int',N'EXEC ASC_GETINTAKERECORDITEMS @P0 ',1000 select @p1`,
			wantOK: true,
			want:   "EXEC ASC_GETINTAKERECORDITEMS 1000",
		},
		{
			name: "sp_prepexec nvarchar param with nested parens",
			in: `declare @p1 int set @p1=11 exec sp_prepexec @p1 output,` +
				`N'@P0 nvarchar(4000)',` +
				`N'SELECT SYSTEMDATEGUID FROM AsSystemDate WHERE (CURRENTINDICATOR = @P0) ',` +
				`N'Y' select @p1`,
			wantOK: true,
			want:   "SELECT SYSTEMDATEGUID FROM AsSystemDate WHERE (CURRENTINDICATOR = 'Y')",
		},
		{
			name: "sp_prepexec multiple params",
			in: `declare @p1 int set @p1=42 exec sp_prepexec @p1 output,` +
				`N'@P0 int, @P1 nvarchar(10)',` +
				`N'SELECT * FROM T WHERE Id = @P0 AND Name = @P1',` +
				`100, N'foo' select @p1`,
			wantOK: true,
			want:   "SELECT * FROM T WHERE Id = 100 AND Name = 'foo'",
		},
		{
			name: "sp_prepexec NULL argument",
			in: `declare @p1 int set @p1=1 exec sp_prepexec @p1 output,` +
				`N'@P0 int',N'SELECT @P0',NULL select @p1`,
			wantOK: true,
			want:   "SELECT NULL",
		},
		{
			name: "sp_prepexec NULL param decl (no params)",
			in: `declare @p1 int set @p1=5490 exec sp_prepexec @p1 output,` +
				`NULL,N'SELECT TOP 1 CodeName FROM AsCode' select @p1`,
			wantOK: true,
			want:   "SELECT TOP 1 CodeName FROM AsCode",
		},
		{
			name: "sp_prepexec doubled-quote escape inside value",
			in: `declare @p1 int set @p1=1 exec sp_prepexec @p1 output,` +
				`N'@P0 nvarchar(50)',N'SELECT @P0',N'O''Brien' select @p1`,
			wantOK: true,
			want:   "SELECT 'O''Brien'",
		},
		{
			name:   "sp_executesql with params",
			in:     `exec sp_executesql N'SELECT * FROM T WHERE Id = @P0',N'@P0 int',99`,
			wantOK: true,
			want:   "SELECT * FROM T WHERE Id = 99",
		},
		{
			name:   "sp_executesql without param decl",
			in:     `sp_executesql N'SELECT 1'`,
			wantOK: true,
			want:   "SELECT 1",
		},
		{
			name:   "sp_unprepare untouched",
			in:     `exec sp_unprepare 5089`,
			wantOK: false,
			want:   `exec sp_unprepare 5089`,
		},
		{
			name:   "plain select untouched",
			in:     `SELECT COUNT(*) FROM AsActivity`,
			wantOK: false,
			want:   `SELECT COUNT(*) FROM AsActivity`,
		},
		{
			name:   "bare positional call is passed through untouched",
			in:     `{call asc_GetDepositValueList(?, ?, ?, ?)}`,
			wantOK: false,
			want:   `{call asc_GetDepositValueList(?, ?, ?, ?)}`,
		},
		{
			name:   "whole-word replacement does not touch @P10 when @P1 provided",
			in:     `exec sp_executesql N'SELECT @P1, @P10', N'@P1 int, @P10 int', 1, 2`,
			wantOK: true,
			want:   "SELECT 1, 2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UnwrapRPC(tc.in)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (got=%q)", ok, tc.wantOK, got)
			}
			if got != tc.want {
				t.Errorf("got  = %q\nwant = %q", got, tc.want)
			}
		})
	}
}

// prepexecOf builds the sp_prepexec batch shape a JDBC CallableStatement
// produces, for the given prepared-statement handle.
func prepexecOf(handle int, decl, template, values string) string {
	return "declare @p1 int set @p1=" + itoa(handle) + " exec sp_prepexec @p1 output," +
		"N'" + decl + "',N'" + template + "'," + values + " select @p1"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestPrepexecHandle(t *testing.T) {
	in := prepexecOf(5089, "@P0 int", "EXEC ASC_GETINTAKERECORDITEMS @P0 ", "1000")
	got, ok := prepexecHandle(in)
	if !ok || got != 5089 {
		t.Errorf("prepexecHandle() = %d, %v; want 5089, true", got, ok)
	}
	if _, ok := prepexecHandle("SELECT 1"); ok {
		t.Error("a plain statement must not yield a handle")
	}
}

func TestHandleCacheResolvesSPExecute(t *testing.T) {
	cache := NewHandleCache()

	// The prepare carries the SQL text and the first call's values.
	prepare := toEventFor(prepexecOf(
		5089,
		"@P0 nvarchar(4000),@P1 datetime,@P2 datetime,@P3 int",
		"EXEC asc_GetDepositValueList @P0,@P1,@P2,@P3 ",
		"N'9F1C',N'2026-08-30 00:00:00',N'2026-08-30 00:00:00',100000",
	))
	cache.Observe(prepare)

	// Every later execution reuses the handle and ships values only — the SQL
	// text is absent from the event entirely.
	reuse := toEventFor(`exec sp_execute 5089,N'A2B3',N'2026-08-31 00:00:00',N'2026-08-31 00:00:00',7`)
	cache.Resolve(&reuse)

	want := "EXEC asc_GetDepositValueList 'A2B3','2026-08-31 00:00:00','2026-08-31 00:00:00',7"
	if reuse.SQL != want {
		t.Errorf("resolved SQL\n got = %q\nwant = %q", reuse.SQL, want)
	}
	if reuse.ParamsUnavailable {
		t.Error("a resolved handle must not be flagged params-unavailable")
	}
	// The rewritten SQL must be re-derived, or --type/--table still miss it.
	if reuse.StatementType != StmtExec {
		t.Errorf("StatementType = %q, want %q", reuse.StatementType, StmtExec)
	}
	if len(reuse.Tables) != 1 || reuse.Tables[0] != "asc_GetDepositValueList" {
		t.Errorf("Tables = %#v, want [asc_GetDepositValueList]", reuse.Tables)
	}
}

func TestHandleCacheUnresolvedHandleIsLabelled(t *testing.T) {
	// The prepare happened before capture started, so the template is
	// unknowable. The values must still be shown, and the gap named — never
	// rendered as though it were the real call.
	cache := NewHandleCache()
	e := toEventFor(`exec sp_execute 91,N'9F1C',100000`)
	cache.Resolve(&e)

	if !e.ParamsUnavailable {
		t.Error("an unresolved handle must set ParamsUnavailable")
	}
	for _, frag := range []string{"unresolved prepared handle 91", "'9F1C'", "100000"} {
		if !strings.Contains(e.SQL, frag) {
			t.Errorf("unresolved rendering %q missing %q", e.SQL, frag)
		}
	}
}

func TestParamsUnavailableDetection(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"positional call the driver did not expand", `{call asc_GetDepositValueList(?, ?, ?, ?)}`, true},
		{"call without braces", `call p(?, ?)`, true},
		{"unsubstituted placeholder left after unwrap", "EXEC p @P0, @P1", true},
		{"fully substituted call", "EXEC p 1, 'x'", false},
		{"plain select", "SELECT * FROM AsActivity", false},
		{"question mark inside a string literal is not a placeholder", "SELECT 'what?' AS note", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUnboundParams(tc.sql); got != tc.want {
				t.Errorf("hasUnboundParams(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
