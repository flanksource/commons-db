package xetrace

import (
	"reflect"
	"testing"
	"time"
)

func TestParseRPC_ExecuteSQL(t *testing.T) {
	raw := `exec sp_executesql N'SELECT * FROM AsUser WHERE ClientNumber = @P0 AND StatusCode = @P1', N'@P0 nvarchar(50),@P1 int', N'alice', 1`
	call, ok := ParseRPC(raw)
	if !ok {
		t.Fatalf("ParseRPC returned ok=false for %q", raw)
	}
	if call.Template != "SELECT * FROM AsUser WHERE ClientNumber = @P0 AND StatusCode = @P1" {
		t.Errorf("unexpected template: %q", call.Template)
	}
	if call.ParamDecl != "@P0 nvarchar(50),@P1 int" {
		t.Errorf("unexpected param decl: %q", call.ParamDecl)
	}
	wantVals := []string{"N'alice'", "1"}
	if !reflect.DeepEqual(call.Values, wantVals) {
		t.Errorf("unexpected values: got %v want %v", call.Values, wantVals)
	}
}

func TestParseRPC_ExecuteSQLWithoutDecl(t *testing.T) {
	raw := `sp_executesql N'SELECT 42'`
	call, ok := ParseRPC(raw)
	if !ok {
		t.Fatalf("ParseRPC ok=false for decl-less executesql")
	}
	if call.Template != "SELECT 42" {
		t.Errorf("template: %q", call.Template)
	}
	if call.ParamDecl != "" {
		t.Errorf("expected empty decl, got %q", call.ParamDecl)
	}
	if len(call.Values) != 0 {
		t.Errorf("expected no values, got %v", call.Values)
	}
}

func TestParseRPC_Prepexec(t *testing.T) {
	raw := `declare @p1 int set @p1=7 exec sp_prepexec @p1 output, N'@P0 int', N'SELECT * FROM AsUser WHERE ClientGUID = @P0', 42 select @p1`
	call, ok := ParseRPC(raw)
	if !ok {
		t.Fatalf("ParseRPC ok=false for sp_prepexec")
	}
	if call.Template != "SELECT * FROM AsUser WHERE ClientGUID = @P0" {
		t.Errorf("template: %q", call.Template)
	}
	if call.ParamDecl != "@P0 int" {
		t.Errorf("decl: %q", call.ParamDecl)
	}
	if !reflect.DeepEqual(call.Values, []string{"42"}) {
		t.Errorf("values: %v", call.Values)
	}
}

func TestParseRPC_PlainStatement(t *testing.T) {
	if _, ok := ParseRPC("SELECT 1 FROM AsUser"); ok {
		t.Error("ParseRPC should return ok=false for plain statements")
	}
	if _, ok := ParseRPC(""); ok {
		t.Error("ParseRPC should return ok=false for empty input")
	}
}

func TestRPCCall_ToQuery_PlaceholderRewrite(t *testing.T) {
	call := &RPCCall{
		Template:  "SELECT * FROM AsUser WHERE ClientNumber = @P0 AND StatusCode = @P1",
		ParamDecl: "@P0 nvarchar(50),@P1 int",
		Values:    []string{"N'alice'", "42"},
	}
	sql, args, warnings := call.ToQuery()
	wantSQL := "SELECT * FROM AsUser WHERE ClientNumber = @p1 AND StatusCode = @p2"
	if sql != wantSQL {
		t.Errorf("sql = %q want %q", sql, wantSQL)
	}
	wantArgs := []any{"alice", int64(42)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v want %#v", args, wantArgs)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestRPCCall_ToQuery_NoDeclInfersFromTemplate(t *testing.T) {
	call := &RPCCall{
		Template: "SELECT @P0 + @P1",
		Values:   []string{"1", "2"},
	}
	sql, args, _ := call.ToQuery()
	if sql != "SELECT @p1 + @p2" {
		t.Errorf("sql = %q", sql)
	}
	if !reflect.DeepEqual(args, []any{int64(1), int64(2)}) {
		t.Errorf("args = %v", args)
	}
}

func TestRPCCall_ToQuery_MissingValuesFillNil(t *testing.T) {
	call := &RPCCall{
		Template:  "SELECT @P0, @P1",
		ParamDecl: "@P0 int, @P1 int",
		Values:    []string{"1"},
	}
	_, args, _ := call.ToQuery()
	if len(args) != 2 || args[0] != int64(1) || args[1] != nil {
		t.Errorf("args = %v, want [1, nil]", args)
	}
}

func TestCoerceValue(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		sqlType string
		want    any
		warnSub string // substring expected in the warning, or "" for no warning
	}{
		{"null literal", "NULL", "int", nil, ""},
		{"null lowercase", "null", "nvarchar", nil, ""},
		{"empty token", "", "int", nil, ""},

		{"int", "42", "int", int64(42), ""},
		{"bigint", "9223372036854775807", "bigint", int64(9223372036854775807), ""},
		{"smallint negative", "-7", "smallint", int64(-7), ""},
		{"int unparseable", "abc", "int", "abc", "could not parse"},

		{"bit false", "0", "bit", false, ""},
		{"bit true", "1", "bit", true, ""},
		{"bit invalid", "2", "bit", "2", "could not parse"},

		{"float", "3.14", "float", 3.14, ""},
		{"decimal", "99.99", "decimal", 99.99, ""},
		{"money", "12.5", "money", 12.5, ""},

		{"nvarchar string", "N'alice'", "nvarchar", "alice", ""},
		{"varchar string", "'bob'", "varchar", "bob", ""},
		{"uniqueidentifier string", "N'6ee9663f-2b30-47fa-bb4b-76879256150a'", "uniqueidentifier", "6ee9663f-2b30-47fa-bb4b-76879256150a", ""},
		{"empty string literal", "N''", "nvarchar", "", ""},

		{"datetime", "'2024-01-15 12:34:56'", "datetime", mustParseTime(t, "2006-01-02 15:04:05", "2024-01-15 12:34:56"), ""},
		{"datetime2 with fraction", "'2024-01-15 12:34:56.1234567'", "datetime2", mustParseTime(t, "2006-01-02 15:04:05.9999999", "2024-01-15 12:34:56.1234567"), ""},
		{"date only", "'2024-01-15'", "date", mustParseTime(t, "2006-01-02", "2024-01-15"), ""},
		{"bad datetime falls back with warning", "'not-a-date'", "datetime", "not-a-date", "could not parse"},

		{"hex binary", "0x1A2B", "varbinary", []byte{0x1A, 0x2B}, ""},

		{"unknown numeric type", "42", "numeric_weird", "42", "unknown numeric type"},
		{"unknown string type", "N'foo'", "weird_type", "foo", "unknown string type"},

		{"no type hint int", "42", "", int64(42), ""},
		{"no type hint float", "3.14", "", 3.14, ""},
		{"no type hint string", "N'foo'", "", "foo", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, warn := coerceValue(c.raw, c.sqlType)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("value = %#v want %#v", got, c.want)
			}
			if c.warnSub == "" && warn != "" {
				t.Errorf("unexpected warning: %q", warn)
			}
			if c.warnSub != "" && !containsIgnoreCase(warn, c.warnSub) {
				t.Errorf("warning %q does not contain %q", warn, c.warnSub)
			}
		})
	}
}

func TestParseParamDecl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []paramInfo
	}{
		{"empty", "", nil},
		{
			"single",
			"@P0 int",
			[]paramInfo{{Name: "@P0", SQLType: "int"}},
		},
		{
			"two with comma",
			"@P0 nvarchar(50),@P1 int",
			[]paramInfo{{"@P0", "nvarchar"}, {"@P1", "int"}},
		},
		{
			"spaces and length",
			"@P0 varchar(100) , @P1 datetime2(7)",
			[]paramInfo{{"@P0", "varchar"}, {"@P1", "datetime2"}},
		},
		{
			"mixed case normalized",
			"@P0 INT, @P1 NVarChar(10)",
			[]paramInfo{{"@P0", "int"}, {"@P1", "nvarchar"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseParamDecl(c.in)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestIsReplayable(t *testing.T) {
	cases := []struct {
		name     string
		event    Event
		template string
		want     bool
		reason   string
	}{
		{
			"plain SELECT",
			Event{Name: EventSQLStatementCompleted, Statement: "SELECT * FROM AsUser"},
			"SELECT * FROM AsUser",
			true, "",
		},
		{
			"SELECT with parameters",
			Event{Name: EventRPCCompleted},
			"SELECT * FROM AsUser WHERE ClientGUID = @P0",
			true, "",
		},
		{
			"SELECT INTO rejected",
			Event{Name: EventSQLStatementCompleted},
			"SELECT * INTO #tmp FROM AsUser",
			false, "INTO",
		},
		{
			// It refers to the procedure's own parameters and locals, which the
			// trace does not carry, so replaying it can only error.
			"statement inside a stored procedure rejected",
			Event{Name: EventSPStatementCompleted},
			"SELECT * FROM AsDepositValue WHERE PolicyGuid = @guidPolicyGUID",
			false, "inside a stored procedure",
		},
		{
			"statement with uncaptured parameters rejected",
			Event{Name: EventRPCCompleted, ParamsUnavailable: true},
			"SELECT * FROM AsUser WHERE ClientGUID = @P0",
			false, "parameters not captured",
		},
		{
			"SELECT with INTRODUCTION column survives",
			Event{Name: EventSQLStatementCompleted},
			"SELECT INTRODUCTION_DATE FROM AsUser",
			true, "",
		},
		{
			"SELECT with EXEC rejected",
			Event{Name: EventSQLStatementCompleted},
			"SELECT * FROM AsUser; EXEC sp_stuff",
			false, "EXEC",
		},
		{
			"SELECT with EXECUTION column survives",
			Event{Name: EventSQLStatementCompleted},
			"SELECT EXECUTION_PLAN FROM AsActivity",
			true, "",
		},
		{
			"INSERT rejected",
			Event{Name: EventSQLStatementCompleted},
			"INSERT INTO AsUser VALUES (1)",
			false, "not a SELECT",
		},
		{
			"UPDATE rejected",
			Event{Name: EventSQLStatementCompleted},
			"UPDATE AsUser SET UserStatus = '01'",
			false, "not a SELECT",
		},
		{
			"DELETE rejected",
			Event{Name: EventSQLStatementCompleted},
			"DELETE FROM AsUser WHERE ClientNumber = 'foo'",
			false, "not a SELECT",
		},
		{
			"error event rejected",
			Event{Name: EventErrorReported, Statement: "SELECT 1"},
			"SELECT 1",
			false, "error event",
		},
		{
			"empty statement rejected",
			Event{Name: EventSQLStatementCompleted},
			"",
			false, "empty statement",
		},
		{
			"leading whitespace",
			Event{Name: EventSQLStatementCompleted},
			"\n  SELECT * FROM AsUser",
			true, "",
		},
		{
			"lowercase select ok",
			Event{Name: EventSQLStatementCompleted},
			"select * from AsUser",
			true, "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := isReplayable(c.event, c.template)
			if got != c.want {
				t.Errorf("isReplayable = %v, want %v (reason=%q)", got, c.want, reason)
			}
			if c.reason != "" && !containsIgnoreCase(reason, c.reason) {
				t.Errorf("reason %q does not contain %q", reason, c.reason)
			}
		})
	}
}

func TestFormatScanValue(t *testing.T) {
	// d0 da 0f 87 9b be 47 16 99 30 51 2e ee f2 e6 14 → canonical
	// lowercase d0da0f87-9bbe-4716-9930-512eeef2e614. These are the bytes
	// the go-mssqldb driver writes into an interface{} scan target after
	// applying its own wire-to-display byte swap.
	guidBytes := []byte{
		0xd0, 0xda, 0x0f, 0x87,
		0x9b, 0xbe,
		0x47, 0x16,
		0x99, 0x30,
		0x51, 0x2e, 0xee, 0xf2, 0xe6, 0x14,
	}

	cases := []struct {
		name   string
		in     any
		dbType string
		want   any
	}{
		{
			"uniqueidentifier 16 bytes",
			guidBytes,
			"UNIQUEIDENTIFIER",
			"d0da0f87-9bbe-4716-9930-512eeef2e614",
		},
		{
			"uniqueidentifier lowercase dbtype",
			guidBytes,
			"uniqueidentifier",
			"d0da0f87-9bbe-4716-9930-512eeef2e614",
		},
		{
			"nvarchar bytes",
			[]byte("hello world"),
			"NVARCHAR",
			"hello world",
		},
		{
			"varchar bytes",
			[]byte("hi"),
			"VARCHAR",
			"hi",
		},
		{
			"varbinary bytes",
			[]byte{0x01, 0x02, 0x03, 0x04},
			"VARBINARY",
			"0x01020304",
		},
		{
			"binary bytes",
			[]byte{0xAB, 0xCD},
			"BINARY",
			"0xABCD",
		},
		{
			"unknown type invalid utf8 falls back to hex",
			[]byte{0xFF, 0xFE},
			"SOMETHING_NEW",
			"0xFFFE",
		},
		{
			"unknown type valid utf8 stringifies",
			[]byte("ascii"),
			"SOMETHING_NEW",
			"ascii",
		},
		{
			"int passes through",
			int64(42),
			"INT",
			int64(42),
		},
		{
			"time passes through",
			time.Date(2024, 1, 15, 12, 34, 56, 0, time.UTC),
			"DATETIME",
			time.Date(2024, 1, 15, 12, 34, 56, 0, time.UTC),
		},
		{
			"nil passes through",
			nil,
			"NVARCHAR",
			nil,
		},
		{
			"short bytes with uniqueidentifier type hex fallback",
			[]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
			"UNIQUEIDENTIFIER",
			"0x0102030405060708",
		},
		{
			"empty db type with utf8 bytes stringifies",
			[]byte("fallback"),
			"",
			"fallback",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatScanValue(c.in, c.dbType)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("formatScanValue(%v, %q) = %#v, want %#v",
					c.in, c.dbType, got, c.want)
			}
		})
	}
}

func mustParseTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(layout, value)
	if err != nil {
		t.Fatalf("mustParseTime(%q): %v", value, err)
	}
	return parsed
}

func containsIgnoreCase(haystack, needle string) bool {
	h := make([]byte, len(haystack))
	n := make([]byte, len(needle))
	for i := range haystack {
		c := haystack[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		h[i] = c
	}
	for i := range needle {
		c := needle[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		n[i] = c
	}
	return bytesContains(h, n)
}

func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
