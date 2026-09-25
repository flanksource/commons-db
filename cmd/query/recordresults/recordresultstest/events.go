// Package recordresultstest holds the result types, stores and tenants the
// recordresults specs and the HTTP specs that serve them share.
package recordresultstest

import (
	"time"

	"github.com/flanksource/clicky/api"
)

// SampleEvent is a small result type in the shape a trace capture produces: an
// instant, two low-cardinality dimensions, a measure and a structured detail.
type SampleEvent struct {
	At      time.Time      `json:"at" pretty:"label=Captured"`
	DB      string         `json:"db"`
	User    string         `json:"user"`
	Elapsed float64        `json:"elapsed_ms" pretty:"type=duration,unit=ms"`
	Slow    bool           `json:"slow"`
	Tables  []string       `json:"tables"`
	Detail  map[string]any `json:"detail"`
}

func (SampleEvent) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("at").Label("Captured").Kind("timestamp").Build(),
		api.Column("db").Label("Database").Build(),
		api.Column("user").Label("User").Build(),
		api.Column("elapsed_ms").Label("Elapsed").Build(),
		api.Column("slow").Label("Slow").Build(),
		api.Column("tables").Label("Tables").Kind("tags").Build(),
		api.Column("detail").Hidden().Build(),
	}
}

func (e SampleEvent) Row() map[string]any {
	return map[string]any{
		"at": e.At,
		"db": api.TableCell{
			Value:       api.Text{Content: e.DB, Style: "text-blue-500"},
			FilterValue: e.DB,
		},
		"user": e.User, "elapsed_ms": e.Elapsed, "slow": e.Slow,
		"tables": e.Tables, "detail": e.Detail,
	}
}

var (
	// SampleDBs are the databases sample events cycle through.
	SampleDBs   = []string{"oipa", "audit", "report"}
	sampleUsers = []string{"alice", "bob"}
	sampleStart = time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
)

// SampleEvents are events first..last; event n is n milliseconds after start,
// is slow when n is a multiple of 5, and reads the tables sampleTables(n).
func SampleEvents(first, last int) []SampleEvent {
	events := make([]SampleEvent, 0, last-first+1)
	for n := first; n <= last; n++ {
		events = append(events, SampleEvent{
			At: sampleStart.Add(time.Duration(n) * time.Millisecond), DB: SampleDBs[n%3], User: sampleUsers[n%2],
			Elapsed: float64(n), Slow: n%5 == 0, Tables: sampleTables(n), Detail: map[string]any{"n": n},
		})
	}
	return events
}

// sampleTables is what event n reads: every event reads policy, an even one
// also reads client, and a multiple of 3 also reads activity. Every tenth reads
// nothing at all, which is the row an exclusion must still keep.
func sampleTables(n int) []string {
	if n%10 == 0 {
		return nil
	}
	tables := []string{"policy"}
	if n%2 == 0 {
		tables = append(tables, "client")
	}
	if n%3 == 0 {
		tables = append(tables, "activity")
	}
	return tables
}

// InvocationResult is a keyed result type whose rows form a call hierarchy.
type InvocationResult struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Name     string `json:"name" filter:"exact"`
	Depth    int    `json:"depth"`
}

// Column is the named column of rows, in row order.
func Column(rows []map[string]any, name string) []any {
	values := make([]any, len(rows))
	for index, row := range rows {
		values[index] = row[name]
	}
	return values
}
