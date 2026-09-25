package xetrace

import (
	"strings"
	"testing"
	"time"
)

func fixtureEvent(overrides func(*Event)) Event {
	ts, _ := time.Parse(time.RFC3339Nano, "2026-04-15T10:23:45.123Z")
	e := Event{
		Name:         EventSQLStatementCompleted,
		Timestamp:    ts,
		Duration:     12 * time.Millisecond,
		CPUTime:      8 * time.Millisecond,
		LogicalReads: 42,
		RowCount:     1,
		DatabaseName: "warehouse",
		Username:     "sa",
		SessionID:    73,
		Statement:    "SELECT 1",
		SQL:          "SELECT 1",
	}
	if overrides != nil {
		overrides(&e)
	}
	return e
}

func TestStreamLine_StatementScoped(t *testing.T) {
	e := fixtureEvent(nil)
	got := StreamLine(e, StreamLineOptions{ShowDatabase: false}).String()

	mustContain := []string{"SELECT 1", "sa"}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in: %s", frag, got)
		}
	}
	mustNotContain := []string{"db=", "sid=", "rows="}
	for _, frag := range mustNotContain {
		if strings.Contains(got, frag) {
			t.Errorf("unexpected %q in: %s", frag, got)
		}
	}
}

func TestStreamLine_InstanceWideShowsDatabase(t *testing.T) {
	e := fixtureEvent(nil)
	got := StreamLine(e, StreamLineOptions{ShowDatabase: true}).String()
	if !strings.Contains(got, "db=") {
		t.Errorf("expected db= marker in instance-wide mode, got: %s", got)
	}
	if !strings.Contains(got, "warehouse") {
		t.Errorf("expected warehouse in instance-wide mode, got: %s", got)
	}
}

func TestStreamLine_MetricsBracket(t *testing.T) {
	cases := []struct {
		name        string
		override    func(*Event)
		wantSubs    []string
		wantNotSubs []string
	}{
		{
			name:     "reads and writes present",
			override: func(e *Event) { e.LogicalReads = 42; e.Writes = 1; e.RowCount = 0 },
			wantSubs: []string{"reads: 42", "writes: 1"},
		},
		{
			name: "no bracket when all zero",
			override: func(e *Event) {
				e.LogicalReads = 0
				e.Writes = 0
				e.PhysicalReads = 0
				e.RowCount = 0
			},
			wantNotSubs: []string{"reads:", "writes:", "rows:", "("},
		},
		{
			name:     "physical and logical reads sum",
			override: func(e *Event) { e.LogicalReads = 40; e.PhysicalReads = 2; e.RowCount = 0 },
			wantSubs: []string{"reads: 42"},
		},
		{
			name:     "high reads still printed",
			override: func(e *Event) { e.LogicalReads = 5000; e.RowCount = 0 },
			wantSubs: []string{"reads: 5000"},
		},
		{
			name:     "rows alone triggers bracket",
			override: func(e *Event) { e.LogicalReads = 0; e.Writes = 0; e.RowCount = 3 },
			wantSubs: []string{"rows: 3"},
		},
		{
			name:     "reads writes and rows together",
			override: func(e *Event) { e.LogicalReads = 42; e.Writes = 1; e.RowCount = 7 },
			wantSubs: []string{"reads: 42", "writes: 1", "rows: 7"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fixtureEvent(tc.override)
			got := StreamLine(e, StreamLineOptions{}).String()
			for _, sub := range tc.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in: %s", sub, got)
				}
			}
			for _, sub := range tc.wantNotSubs {
				if strings.Contains(got, sub) {
					t.Errorf("unexpected %q in: %s", sub, got)
				}
			}
		})
	}
}

func TestStreamLine_ErrorEvent(t *testing.T) {
	e := fixtureEvent(func(e *Event) {
		e.Name = EventErrorReported
		e.Statement = ""
		e.SQL = ""
		e.ErrorNumber = 208
		e.ErrorMessage = "Invalid object name 'NoSuchTable'."
		e.Duration = 0
		e.LogicalReads = 0
	})
	got := StreamLine(e, StreamLineOptions{}).String()
	if !strings.Contains(got, "[208]") {
		t.Errorf("expected [208] in error line, got: %s", got)
	}
	if !strings.Contains(got, "Invalid object name") {
		t.Errorf("expected error message in line, got: %s", got)
	}
}

func TestDurationStyle_Thresholds(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Microsecond, "text-muted"},
		{1 * time.Millisecond, "text-yellow-500"},
		{5 * time.Millisecond, "text-yellow-500"},
		{10 * time.Millisecond, "text-orange-500"},
		{50 * time.Millisecond, "text-orange-500"},
		{100 * time.Millisecond, "text-red-500"},
		{500 * time.Millisecond, "text-red-500"},
		{1 * time.Second, "text-red-500 font-bold"},
		{5 * time.Second, "text-red-500 font-bold"},
	}
	for _, tc := range cases {
		if got := durationStyle(tc.d); got != tc.want {
			t.Errorf("durationStyle(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestMetricStyle_Threshold(t *testing.T) {
	if got := metricStyle(100); got != "text-muted" {
		t.Errorf("metricStyle(100) = %q, want text-muted", got)
	}
	if got := metricStyle(101); got != "text-orange-500" {
		t.Errorf("metricStyle(101) = %q, want text-orange-500", got)
	}
}
