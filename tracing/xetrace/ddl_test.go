package xetrace

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildCreateSQL_AllEventsScoped(t *testing.T) {
	opts := CreateOptions{
		Name:              "commons_db_trace_test",
		DatabaseName:      "warehouse",
		Users:             []string{"sa"},
		MinDurationMicros: 1000,
		ExcludeSessionID:  55,
		Events:            DefaultEvents,
		MaxMemoryKB:       4096,
		MaxEvents:         1000,
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}

	mustContain := []string{
		"CREATE EVENT SESSION [commons_db_trace_test] ON SERVER",
		"ADD EVENT sqlserver.sql_statement_completed",
		"ADD EVENT sqlserver.rpc_completed",
		"ADD EVENT sqlserver.sql_batch_completed",
		"ADD EVENT sqlserver.error_reported",
		"sqlserver.database_name = N'warehouse'",
		"sqlserver.username = N'sa'",
		"sqlserver.session_id <> 55",
		"duration >= 1000",
		"ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 1000)",
		"MAX_DISPATCH_LATENCY = 1 SECONDS",
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("output missing fragment %q\nfull:\n%s", frag, got)
		}
	}

	// error_reported has no duration field — predicate must not appear on it.
	errClause := extractEventClause(t, got, "error_reported")
	if strings.Contains(errClause, "duration >=") {
		t.Errorf("error_reported should not have duration predicate:\n%s", errClause)
	}

	// sql_statement_completed must still have the duration predicate.
	stmtClause := extractEventClause(t, got, "sql_statement_completed")
	if !strings.Contains(stmtClause, "duration >= 1000") {
		t.Errorf("sql_statement_completed should have duration predicate:\n%s", stmtClause)
	}
}

// TestDefaultEventsExcludeSPStatement is the opt-in guard. sp_statement_completed
// emits one event per statement inside every procedure; against a 1000-event ring
// buffer that displaces the calls the user actually asked for. It must stay
// reachable only by naming it explicitly via --event.
func TestDefaultEventsExcludeSPStatement(t *testing.T) {
	for _, e := range DefaultEvents {
		if e == EventSPStatementCompleted {
			t.Fatalf("%s must not be in DefaultEvents — it is opt-in via --event", e)
		}
	}
	if !slices.Contains(SupportedEvents, EventSPStatementCompleted) {
		t.Errorf("%s must be in SupportedEvents so NormalizeEvents accepts it", EventSPStatementCompleted)
	}
}

// TestBuildCreateSQL_SPStatementEnablesCausality covers both halves of the
// opt-in: the new event carries the duration predicate (without it every
// trivial inner statement is captured), and requesting it flips
// TRACK_CAUSALITY on so inner statements can be grouped under the RPC that
// ran them. Not requesting it must leave the DDL exactly as it was.
func TestBuildCreateSQL_SPStatementEnablesCausality(t *testing.T) {
	base := CreateOptions{Name: "s", MinDurationMicros: 1000, MaxMemoryKB: 1024, MaxEvents: 100}

	withoutSP := base
	withoutSP.Events = DefaultEvents
	got, err := BuildCreateSQL(withoutSP)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "TRACK_CAUSALITY = OFF") {
		t.Errorf("default events must leave causality off:\n%s", got)
	}
	if strings.Contains(got, "attach_activity_id") {
		t.Errorf("default events must not add the causality action:\n%s", got)
	}

	withSP := base
	withSP.Events = append(append([]string(nil), DefaultEvents...), EventSPStatementCompleted)
	got, err = BuildCreateSQL(withSP)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "TRACK_CAUSALITY = ON") {
		t.Errorf("sp_statement_completed must enable causality:\n%s", got)
	}
	if !strings.Contains(got, "package0.attach_activity_id") {
		t.Errorf("causality requires the attach_activity_id action:\n%s", got)
	}
	spClause := extractEventClause(t, got, "sp_statement_completed")
	if !strings.Contains(spClause, "duration >= 1000") {
		t.Errorf("sp_statement_completed must carry the duration predicate:\n%s", spClause)
	}
}

// extractEventClause returns the substring of ddl covering a single
// ADD EVENT sqlserver.<name> ( ... ) block. The returned slice stops at the
// closing ')' that matches the event's opening paren.
func extractEventClause(t *testing.T, ddl, name string) string {
	t.Helper()
	marker := "ADD EVENT sqlserver." + name + " ("
	start := strings.Index(ddl, marker)
	if start < 0 {
		t.Fatalf("event %q not found in ddl", name)
	}
	depth := 0
	for i := start + len(marker) - 1; i < len(ddl); i++ {
		switch ddl[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return ddl[start : i+1]
			}
		}
	}
	t.Fatalf("unterminated event clause for %q", name)
	return ""
}

func TestBuildCreateSQL_NoFiltersNoWhereClause(t *testing.T) {
	opts := CreateOptions{
		Name:        "s",
		Events:      []string{EventSQLStatementCompleted},
		MaxMemoryKB: 1024,
		MaxEvents:   100,
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if strings.Contains(got, "WHERE") {
		t.Errorf("expected no WHERE clause, got:\n%s", got)
	}
}

func TestBuildCreateSQL_EscapesUsername(t *testing.T) {
	opts := CreateOptions{
		Name:        "s",
		Users:       []string{"DOMAIN\\o'brien"},
		Events:      []string{EventSQLStatementCompleted},
		MaxMemoryKB: 1024,
		MaxEvents:   100,
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "sqlserver.username = N'DOMAIN\\o''brien'") {
		t.Errorf("expected escaped username predicate, got:\n%s", got)
	}
}

func TestBuildMatchPredicate(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     string
	}{
		{"empty", nil, ""},
		{"star matches all", []string{"*"}, ""},
		{"single exact", []string{"sa"}, "(sqlserver.username = N'sa')"},
		{"multi exact OR", []string{"sa", "app"}, "(sqlserver.username = N'sa' OR sqlserver.username = N'app')"},
		{"prefix wildcard", []string{"app*"}, "(sqlserver.like_i_sql_unicode_string(sqlserver.username, N'app%'))"},
		{"exclusion exact", []string{"!sa"}, "(sqlserver.username <> N'sa')"},
		{"exclusion wildcard", []string{"!app*"}, "(NOT sqlserver.like_i_sql_unicode_string(sqlserver.username, N'app%'))"},
		{
			"positive and negative",
			[]string{"sa", "!app*"},
			"(sqlserver.username = N'sa') AND (NOT sqlserver.like_i_sql_unicode_string(sqlserver.username, N'app%'))",
		},
		{
			"comma list is equivalent to repeated values",
			[]string{"sa, !app*"},
			"(sqlserver.username = N'sa') AND (NOT sqlserver.like_i_sql_unicode_string(sqlserver.username, N'app%'))",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildMatchPredicate("sqlserver.username", tc.patterns); got != tc.want {
				t.Errorf("buildMatchPredicate(%v) = %q, want %q", tc.patterns, got, tc.want)
			}
		})
	}
}

func TestBuildCreateSQL_RequiresName(t *testing.T) {
	_, err := BuildCreateSQL(CreateOptions{Events: DefaultEvents})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestBuildCreateSQL_RequiresEvents(t *testing.T) {
	_, err := BuildCreateSQL(CreateOptions{Name: "x"})
	if err == nil {
		t.Fatal("expected error for missing events")
	}
}

func TestBuildCreateSQL_RejectsNegativeRingBufferSizing(t *testing.T) {
	cases := []struct {
		name string
		opts CreateOptions
	}{
		{"negative memory", CreateOptions{Name: "x", Events: DefaultEvents, MaxMemoryKB: -1}},
		{"negative event cap", CreateOptions{Name: "x", Events: DefaultEvents, MaxEvents: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildCreateSQL(tc.opts); err == nil {
				t.Fatal("expected a negative ring-buffer size to be rejected before it reaches SQL Server")
			}
		})
	}
}

func TestBuildCreateSQL_AppAndHostPredicates(t *testing.T) {
	cases := []struct {
		name string
		opts CreateOptions
		want string
	}{
		{
			"excluded app",
			CreateOptions{Apps: []string{"!go/reporting"}},
			"(sqlserver.client_app_name <> N'go/reporting')",
		},
		{
			"escaped app",
			CreateOptions{Apps: []string{"!weird'app"}},
			"(sqlserver.client_app_name <> N'weird''app')",
		},
		{
			"included app wildcard",
			CreateOptions{Apps: []string{"jTDS*"}},
			"(sqlserver.like_i_sql_unicode_string(sqlserver.client_app_name, N'jTDS%'))",
		},
		{
			"host list",
			CreateOptions{Hosts: []string{"app-0", "!build-agent"}},
			"(sqlserver.client_hostname = N'app-0') AND (sqlserver.client_hostname <> N'build-agent')",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Name = "s"
			opts.Events = []string{EventSQLStatementCompleted}
			opts.MaxMemoryKB, opts.MaxEvents = 1024, 100
			got, err := BuildCreateSQL(opts)
			if err != nil {
				t.Fatalf("BuildCreateSQL: %v", err)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("output missing predicate %q\nfull:\n%s", tc.want, got)
			}
		})
	}
}

// Both filterable client columns must be captured as actions, or the app/host
// filters in the trace viewer have nothing to read.
func TestBuildCreateSQL_CapturesClientActions(t *testing.T) {
	got, err := BuildCreateSQL(CreateOptions{
		Name:        "s",
		Events:      []string{EventSQLStatementCompleted},
		MaxMemoryKB: 1024,
		MaxEvents:   100,
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	for _, action := range []string{"sqlserver.client_app_name", "sqlserver.client_hostname", "sqlserver.username"} {
		if !strings.Contains(got, "ACTION ("+action) && !strings.Contains(got, ", "+action) {
			t.Errorf("ddl missing action %q:\n%s", action, got)
		}
	}
}

func TestBuildCreateSQL_EscapesDatabaseName(t *testing.T) {
	opts := CreateOptions{
		Name:         "s",
		DatabaseName: "weird'name",
		Events:       []string{EventSQLStatementCompleted},
		MaxMemoryKB:  1024,
		MaxEvents:    100,
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "N'weird''name'") {
		t.Errorf("expected escaped quote in db name, got:\n%s", got)
	}
}

// An empty DatabaseName already means "the connection's database", so
// instance-wide capture is asked for explicitly. Naming both is a contradiction
// rather than a preference, and guessing either way would silently change what
// the session captures.
func TestBuildCreateSQL_AllDatabasesEmitsNoDatabasePredicate(t *testing.T) {
	sql, err := BuildCreateSQL(CreateOptions{
		Name: "trace_all", Events: DefaultEvents, AllDatabases: true,
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	// database_name is also captured as an ACTION on every event, so the check
	// is for the WHERE predicate specifically, not any mention of the field.
	if strings.Contains(sql, "sqlserver.database_name = N'") {
		t.Fatalf("an instance-wide capture must not scope by database:\n%s", sql)
	}
}

func TestBuildCreateSQL_RejectsDatabaseWithAllDatabases(t *testing.T) {
	_, err := BuildCreateSQL(CreateOptions{
		Name: "trace_all", Events: DefaultEvents, DatabaseName: "warehouse", AllDatabases: true,
	})
	if err == nil {
		t.Fatal("naming a database and asking for every database must be refused")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
}
