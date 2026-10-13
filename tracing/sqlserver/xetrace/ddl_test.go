package xetrace

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildCreateSQL_AllEventsScoped(t *testing.T) {
	opts := CreateOptions{
		Name:              "commons_db_trace_test",
		Databases:         []string{"warehouse"},
		ExcludeSessionID:  55,
		Users:             []string{"sa"},
		MinDurationMicros: 1000,
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
		"sqlserver.equal_i_sql_unicode_string(sqlserver.database_name, N'warehouse')",
		"sqlserver.equal_i_sql_unicode_string(sqlserver.username, N'sa')",
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
		t.Errorf("default events must not name the causality action:\n%s", got)
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
	// package0.attach_activity_id is a PRIVATE action: naming it in an ACTION
	// list makes SQL Server reject the whole CREATE ("The event action name,
	// "package0.attach_activity_id", is invalid, or the object could not be
	// found"), so every sp_statement_completed capture failed to start.
	// TRACK_CAUSALITY = ON is the only way to ask for it.
	if strings.Contains(got, "package0.attach_activity_id") {
		t.Errorf("the private causality action must never be named in an ACTION list:\n%s", got)
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

// With no filters nothing constrains the event, so it carries no WHERE at all:
// the library adds no exclusion a caller did not ask for.
func TestBuildCreateSQL_NoFiltersEmitsNoPredicate(t *testing.T) {
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
	if clause := extractEventClause(t, got, EventSQLStatementCompleted); strings.Contains(clause, "WHERE") {
		t.Errorf("expected no predicate, got:\n%s", clause)
	}
}

// duration is the cheapest test SQL Server can make — an intrinsic field, no
// action to collect — so it leads the WHERE and short-circuits the rest.
func TestBuildCreateSQL_DurationIsTheFirstPredicate(t *testing.T) {
	got, err := BuildCreateSQL(CreateOptions{
		Name: "s", Events: []string{EventSQLStatementCompleted}, MinDurationMicros: 1000,
		Databases: []string{"warehouse"}, Users: []string{"sa"}, Hosts: []string{"cycle*"},
		MaxMemoryKB: 1024, MaxEvents: 100,
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if clause := extractEventClause(t, got, EventSQLStatementCompleted); !strings.Contains(clause, "WHERE (duration >= 1000 AND ") {
		t.Errorf("duration must be the first predicate:\n%s", clause)
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
	if !strings.Contains(got, "sqlserver.equal_i_sql_unicode_string(sqlserver.username, N'DOMAIN\\o''brien')") {
		t.Errorf("expected escaped username predicate, got:\n%s", got)
	}
}

// The session predicate must keep exactly the values collections.MatchItems
// keeps for the same patterns, or the capture and the stored results disagree.
func TestBuildMatchPredicate(t *testing.T) {
	const field = "sqlserver.client_hostname"
	eq := func(v string) string { return "sqlserver.equal_i_sql_unicode_string(" + field + ", N'" + v + "')" }
	like := func(v string) string { return "sqlserver.like_i_sql_unicode_string(" + field + ", N'" + v + "')" }
	cases := []struct {
		name     string
		patterns []string
		want     string
		err      string
	}{
		{name: "empty", patterns: nil, want: ""},
		{name: "star matches all", patterns: []string{"*"}, want: ""},
		{name: "star swallows the other positives", patterns: []string{"cycle-0", "*"}, want: ""},
		{name: "exact is case-insensitive equality", patterns: []string{"cycle-0"}, want: "(" + eq("cycle-0") + ")"},
		{name: "positives are OR'd", patterns: []string{"cycle-0", "orders-0"}, want: "(" + eq("cycle-0") + " OR " + eq("orders-0") + ")"},
		{name: "prefix wildcard", patterns: []string{"cycle*"}, want: "(" + like("cycle%") + ")"},
		{name: "suffix wildcard", patterns: []string{"*-0"}, want: "(" + like("%-0") + ")"},
		{name: "contains wildcard", patterns: []string{"*app*"}, want: "(" + like("%app%") + ")"},
		{name: "LIKE metacharacters are literal", patterns: []string{"oma_zim[1]%25*"}, want: "(" + like("oma[_]zim[[]1][%]%") + ")"},
		{name: "exact values keep their metacharacters", patterns: []string{"oma_zim"}, want: "(" + eq("oma_zim") + ")"},
		{name: "quotes are escaped", patterns: []string{"o'brien*"}, want: "(" + like("o''brien%") + ")"},
		{name: "exclusion-only matches everything else", patterns: []string{"!cycle*", "!build"}, want: "(NOT (" + like("cycle%") + ") AND NOT (" + eq("build") + "))"},
		{
			name:     "an exclusion wins over a positive",
			patterns: []string{"cycle*", "!cycle-1"},
			want:     "(" + like("cycle%") + ") AND (NOT (" + eq("cycle-1") + "))",
		},
		{
			name:     "comma list is equivalent to repeated values",
			patterns: []string{"cycle* , !cycle-1"},
			want:     "(" + like("cycle%") + ") AND (NOT (" + eq("cycle-1") + "))",
		},
		{name: "values are URL-unescaped", patterns: []string{"a%2Cb"}, want: "(" + eq("a,b") + ")"},
		{name: "a mid-pattern star is refused", patterns: []string{"cycle*0"}, err: `"cycle*0"`},
		{name: "a mid-pattern star is refused when negated", patterns: []string{"!a*b*"}, err: `"!a*b*"`},
		{name: "an exclusion of nothing is refused", patterns: []string{"!"}, err: `"!"`},
		{name: "excluding everything is refused", patterns: []string{"!*"}, err: `"!*"`},
		{name: "a bad escape is refused", patterns: []string{"a%zz"}, err: `"a%zz"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildMatchPredicate(field, tc.patterns)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("buildMatchPredicate(%v) error = %v, want one naming %s", tc.patterns, err, tc.err)
				}
				if vErr := ValidateMatchPatterns(tc.patterns); vErr == nil {
					t.Errorf("ValidateMatchPatterns(%v) accepted what the predicate builder refuses", tc.patterns)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildMatchPredicate(%v): %v", tc.patterns, err)
			}
			if got != tc.want {
				t.Errorf("buildMatchPredicate(%v)\n got %q\nwant %q", tc.patterns, got, tc.want)
			}
		})
	}
}

func TestBuildCreateSQL_RefusesAPatternSQLServerCannotMatch(t *testing.T) {
	_, err := BuildCreateSQL(CreateOptions{
		Name: "s", Events: DefaultEvents, Hosts: []string{"cycle*0"}, MaxMemoryKB: 1024, MaxEvents: 100,
	})
	if err == nil || !strings.Contains(err.Error(), `"cycle*0"`) {
		t.Fatalf("BuildCreateSQL error = %v, want the mid-pattern star refused", err)
	}
}

func TestBuildCreateSQL_DatabasePredicates(t *testing.T) {
	const field = "sqlserver.database_name"
	eq := func(v string) string { return "sqlserver.equal_i_sql_unicode_string(" + field + ", N'" + v + "')" }
	like := func(v string) string { return "sqlserver.like_i_sql_unicode_string(" + field + ", N'" + v + "')" }
	cases := []struct {
		name      string
		databases []string
		want      string
	}{
		{"single", []string{"warehouse"}, "(" + eq("warehouse") + ")"},
		{"multiple", []string{"warehouse", "warehouse_audit"}, "(" + eq("warehouse") + " OR " + eq("warehouse_audit") + ")"},
		{"pattern", []string{"sales_eu_west_*"}, "(" + like("sales[_]eu[_]west[_]%") + ")"},
		{"exclusion-only", []string{"!master", "!tempdb"}, "(NOT (" + eq("master") + ") AND NOT (" + eq("tempdb") + "))"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildCreateSQL(CreateOptions{
				Name: "s", Events: []string{EventSQLStatementCompleted}, Databases: tc.databases, MaxMemoryKB: 1024, MaxEvents: 100,
			})
			if err != nil {
				t.Fatalf("BuildCreateSQL: %v", err)
			}
			if !strings.Contains(got, "WHERE ("+tc.want) {
				t.Errorf("output missing leading database predicate %q\nfull:\n%s", tc.want, got)
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
			"(NOT (sqlserver.equal_i_sql_unicode_string(sqlserver.client_app_name, N'go/reporting')))",
		},
		{
			"escaped app",
			CreateOptions{Apps: []string{"!weird'app"}},
			"NOT (sqlserver.equal_i_sql_unicode_string(sqlserver.client_app_name, N'weird''app'))",
		},
		{
			"included app",
			CreateOptions{Apps: []string{"orders-api"}},
			"WHERE ((sqlserver.equal_i_sql_unicode_string(sqlserver.client_app_name, N'orders-api')))",
		},
		{
			"included app wildcard",
			CreateOptions{Apps: []string{"jTDS*"}},
			"(sqlserver.like_i_sql_unicode_string(sqlserver.client_app_name, N'jTDS%'))",
		},
		{
			"host list",
			CreateOptions{Hosts: []string{"app-0", "!build-agent"}},
			"(sqlserver.equal_i_sql_unicode_string(sqlserver.client_hostname, N'app-0')) AND " +
				"(NOT (sqlserver.equal_i_sql_unicode_string(sqlserver.client_hostname, N'build-agent')))",
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

// Every event must carry the session's own sequence number: it is the only
// field that tells two otherwise identical events apart (a connection reset's
// 5701/5703 pair, a driver's repeated `IF @@TRANCOUNT > 0`), and without it the
// drain dedups one of them away and reports it lost.
func TestBuildCreateSQL_NumbersEveryEvent(t *testing.T) {
	for _, events := range [][]string{DefaultEvents, append(append([]string(nil), DefaultEvents...), EventSPStatementCompleted)} {
		got, err := BuildCreateSQL(CreateOptions{Name: "s", Events: events, MaxMemoryKB: 1024, MaxEvents: 100})
		if err != nil {
			t.Fatalf("BuildCreateSQL: %v", err)
		}
		for _, name := range events {
			if clause := extractEventClause(t, got, name); !strings.Contains(clause, "package0.event_sequence") {
				t.Errorf("%s carries no event_sequence action:\n%s", name, clause)
			}
		}
	}
}

func TestBuildCreateSQL_EscapesDatabaseName(t *testing.T) {
	opts := CreateOptions{
		Name:        "s",
		Databases:   []string{"weird'name"},
		Events:      []string{EventSQLStatementCompleted},
		MaxMemoryKB: 1024,
		MaxEvents:   100,
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "N'weird''name'") {
		t.Errorf("expected escaped quote in db name, got:\n%s", got)
	}
}

// "*" is the instance-wide scope: it constrains nothing, so the session carries
// no database predicate at all.
func TestBuildCreateSQL_AllDatabasesEmitsNoDatabasePredicate(t *testing.T) {
	sql, err := BuildCreateSQL(CreateOptions{
		Name: "trace_all", Events: DefaultEvents, Databases: []string{"*"},
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	// database_name is also captured as an ACTION on every event, so the check
	// is for the WHERE predicate specifically, not any mention of the field.
	if strings.Contains(sql, "equal_i_sql_unicode_string(sqlserver.database_name") {
		t.Fatalf("an instance-wide capture must not scope by database:\n%s", sql)
	}
}

// A file target replaces the ring-buffer clause and nothing else: the events,
// predicates, actions and WITH options a capture depends on must be identical
// whichever target holds the events.
func TestBuildCreateSQL_EventFileTarget(t *testing.T) {
	base := CreateOptions{Name: "s", Databases: []string{"warehouse"}, Events: DefaultEvents, MinDurationMicros: 1000}

	ring := base
	ring.MaxMemoryKB, ring.MaxEvents = 4096, 8192
	ringDDL, err := BuildCreateSQL(ring)
	if err != nil {
		t.Fatalf("BuildCreateSQL(ring): %v", err)
	}

	file := base
	file.File = &FileTarget{Path: `D:\rdsdbdata\log\s.xel`, MaxFileSizeMB: 256, MaxRolloverFiles: 10}
	fileDDL, err := BuildCreateSQL(file)
	if err != nil {
		t.Fatalf("BuildCreateSQL(file): %v", err)
	}

	const wantTarget = `ADD TARGET package0.event_file (SET filename = N'D:\rdsdbdata\log\s.xel', max_file_size = 256, max_rollover_files = 10)`
	if !strings.Contains(fileDDL, wantTarget) {
		t.Errorf("missing event_file target clause:\n%s", fileDDL)
	}
	if strings.Contains(fileDDL, "ring_buffer") {
		t.Errorf("file target must not also add a ring buffer:\n%s", fileDDL)
	}
	if !strings.Contains(fileDDL, "MAX_DISPATCH_LATENCY = 1 SECONDS") {
		t.Errorf("dispatch latency must be unchanged:\n%s", fileDDL)
	}

	ringTarget := "ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 8192)"
	if normalized := strings.Replace(fileDDL, wantTarget, ringTarget, 1); normalized != ringDDL {
		t.Errorf("the target clause must be the ONLY difference.\nfile:\n%s\nring:\n%s", fileDDL, ringDDL)
	}
}

func TestBuildCreateSQL_EventFileTargetRejections(t *testing.T) {
	cases := []struct {
		name  string
		opts  CreateOptions
		error string
	}{
		{
			name:  "an unresolved auto path never reaches the DDL",
			opts:  CreateOptions{File: &FileTarget{Path: AutoPath}},
			error: `event_file path "auto"`,
		},
		{
			name:  "a relative path",
			opts:  CreateOptions{File: &FileTarget{Path: "log/s.xel"}},
			error: "must be absolute",
		},
		{
			name:  "the wrong extension",
			opts:  CreateOptions{File: &FileTarget{Path: "/var/opt/mssql/log/s.trc"}},
			error: "must end in .xel",
		},
		{
			name:  "a negative file size",
			opts:  CreateOptions{File: &FileTarget{Path: "/var/opt/mssql/log/s.xel", MaxFileSizeMB: -1}},
			error: "max_file_size -1 MB",
		},
		{
			name:  "a negative rollover count",
			opts:  CreateOptions{File: &FileTarget{Path: "/var/opt/mssql/log/s.xel", MaxRolloverFiles: -2}},
			error: "max_rollover_files -2",
		},
		{
			name:  "ring-buffer sizing alongside a file target",
			opts:  CreateOptions{MaxMemoryKB: 4096, File: &FileTarget{Path: "/var/opt/mssql/log/s.xel"}},
			error: "do not apply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Name, opts.Events = "s", DefaultEvents
			_, err := BuildCreateSQL(opts)
			if err == nil || !strings.Contains(err.Error(), tc.error) {
				t.Fatalf("BuildCreateSQL error = %v, want containing %q", err, tc.error)
			}
		})
	}
}

// The path is a string literal in the DDL like any other.
func TestBuildCreateSQL_EscapesEventFilePath(t *testing.T) {
	got, err := BuildCreateSQL(CreateOptions{
		Name:   "s",
		Events: []string{EventSQLStatementCompleted},
		File:   &FileTarget{Path: "/var/opt/mssql/log/o'brien.xel", MaxFileSizeMB: 1, MaxRolloverFiles: 1},
	})
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	if !strings.Contains(got, "N'/var/opt/mssql/log/o''brien.xel'") {
		t.Errorf("expected escaped quote in the file path, got:\n%s", got)
	}
}
