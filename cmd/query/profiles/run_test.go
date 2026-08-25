package profiles

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// runKeyedMock pages by cursor over a stable id order, resuming from the key the
// engine decoded rather than from an offset — the property a `run --cursor` walk
// depends on and an offset mock could not exercise.
type runKeyedMock struct {
	rows []query.Row
}

func (m *runKeyedMock) Type() string { return "run-keyed" }

func (m *runKeyedMock) PagingModes() query.PagingMode {
	return query.PagingOffset | query.PagingCursor
}

func (m *runKeyedMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}

func (m *runKeyedMock) Pages(_ dbcontext.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	start := page.Offset
	if len(req.Position.Keys) > 0 {
		resume := fmt.Sprint(req.Position.Keys[0])
		for i, row := range m.rows {
			if fmt.Sprint(row["id"]) == resume {
				start = i + 1
				break
			}
		}
	}
	return func(yield func(query.Page, error) bool) {
		total := query.Total{Value: int64(len(m.rows)), Exact: true}
		for ; ; start += page.Limit {
			end := min(start+page.Limit, len(m.rows))
			rows := m.rows[min(start, len(m.rows)):end]
			emitted := query.Page{Rows: rows, HasMore: end < len(m.rows), Total: &total}
			if len(rows) > 0 {
				emitted.NextKeys = []any{rows[len(rows)-1]["id"]}
			}
			if !yield(emitted, nil) || !emitted.HasMore {
				return
			}
		}
	}
}

func newRunService(t *testing.T, profile query.Profile, rows []query.Row) *Service {
	t.Helper()
	query.RegisterProvider(&runKeyedMock{rows: rows})
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func runProfile(name string, limits *query.RowLimits) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "run-keyed"},
		Query:    "rows",
		Columns:  []query.ColumnDef{{Name: "id"}},
		Order:    query.Order{{Column: "id", Unique: true}},
		Limits:   limits,
	}
}

func runRows(count int) []query.Row {
	rows := make([]query.Row, count)
	for i := range rows {
		rows[i] = query.Row{"id": i + 1}
	}
	return rows
}

// The CLI page is the profile's page. A terminal reading a different number of
// rows than the same profile serves over HTTP would be a second paging rule.
func TestRunPageDefaultsToProfilePageSize(t *testing.T) {
	const pageSize = 4
	service := newRunService(t, runProfile("run-default", &query.RowLimits{PageSize: pageSize}), runRows(10))

	result, err := service.Run(context.Background(), "run-default", RunFlags{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Rows) != pageSize {
		t.Fatalf("read %d rows, want the profile's page size %d", len(result.Rows), pageSize)
	}
	if !result.HasMore {
		t.Fatal("a 4-row page of 10 rows must report more available")
	}
	if result.Total == nil || result.Total.Value != 10 || !result.Total.Exact {
		t.Fatalf("total = %+v, want an exact 10", result.Total)
	}
}

// A cursor walk has to cover every row exactly once — the property offset paging
// cannot promise and the reason the CLI resumes by cursor at all.
func TestRunCursorWalkCoversEveryRowOnce(t *testing.T) {
	const total, pageSize = 10, 3
	service := newRunService(t, runProfile("run-walk", &query.RowLimits{PageSize: pageSize}), runRows(total))

	seen := make([]int, 0, total)
	flags := RunFlags{}
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("cursor walk did not terminate")
		}
		result, err := service.Run(context.Background(), "run-walk", flags)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, row := range result.Rows {
			seen = append(seen, row["id"].(int))
		}
		if !result.HasMore {
			break
		}
		if result.NextCursor.IsZero() {
			t.Fatal("a page with more to come must carry the cursor that resumes it")
		}
		flags = RunFlags{Cursor: string(result.NextCursor)}
	}
	if len(seen) != total {
		t.Fatalf("walked %d rows, want %d: %v", len(seen), total, seen)
	}
	for i, id := range seen {
		if id != i+1 {
			t.Fatalf("row %d is id %d, want %d (walk duplicated or skipped): %v", i, id, i+1, seen)
		}
	}
}

// --all reads past the page and stops at the export ceiling, saying so. A read
// that stopped short and reported nothing is the failure the whole contract
// exists to remove.
func TestRunAllStopsAtExportCeilingAndSaysSo(t *testing.T) {
	const ceiling = 7
	service := newRunService(t,
		runProfile("run-all", &query.RowLimits{PageSize: 2, MaxExportRows: ceiling}),
		runRows(20))

	result, err := service.Run(context.Background(), "run-all", RunFlags{All: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Rows) != ceiling {
		t.Fatalf("read %d rows, want the %d-row ceiling", len(result.Rows), ceiling)
	}
	if !result.Truncated {
		t.Fatal("an export that stopped at its ceiling must report it")
	}
	if !strings.Contains(result.Pretty().String(), "stopped short") {
		t.Fatalf("the truncation is not visible on the CLI: %s", result.Pretty().String())
	}
}

// An --all read that fits under the ceiling is complete, and must not claim
// otherwise — a warning on every export is a warning nobody reads.
func TestRunAllUnderCeilingIsNotTruncated(t *testing.T) {
	service := newRunService(t,
		runProfile("run-complete", &query.RowLimits{PageSize: 2, MaxExportRows: 100}),
		runRows(5))

	result, err := service.Run(context.Background(), "run-complete", RunFlags{All: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Rows) != 5 || result.Truncated {
		t.Fatalf("read %d rows truncated=%v, want 5 complete", len(result.Rows), result.Truncated)
	}
}

func TestRunExportRequestRejectsContradictoryFlags(t *testing.T) {
	profile := runProfile("run-flags", &query.RowLimits{PageSize: 10, MaxPageSize: 50})
	for _, tt := range []struct {
		name  string
		flags RunFlags
		want  string
	}{
		{name: "all with limit", flags: RunFlags{All: true, Limit: 5}, want: "cannot be combined"},
		{name: "all with cursor", flags: RunFlags{All: true, Cursor: "abc"}, want: "cannot be combined"},
		{name: "cursor with offset", flags: RunFlags{Cursor: "abc", Offset: 10}, want: "cannot be combined with --offset"},
		{name: "limit above max", flags: RunFlags{Limit: 51}, want: "between 1 and 50"},
		{name: "negative offset", flags: RunFlags{Offset: -1}, want: "zero or greater"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runExportRequest(profile, tt.flags)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// The one number a caller sees is the profile's, on both surfaces: --limit is
// accepted up to the profile's maxPageSize, and --all takes the export ceiling.
func TestRunExportRequestFollowsProfileLimits(t *testing.T) {
	profile := runProfile("run-limits", &query.RowLimits{PageSize: 10, MaxPageSize: 2000, MaxExportRows: 250_000})

	page, err := runExportRequest(profile, RunFlags{Limit: 1500, Offset: 20})
	if err != nil {
		t.Fatalf("runExportRequest: %v", err)
	}
	if page.scope != "page" || page.limit != 1500 || page.offset != 20 {
		t.Fatalf("page request = %+v, want a 1500-row page at offset 20", page)
	}

	all, err := runExportRequest(profile, RunFlags{All: true})
	if err != nil {
		t.Fatalf("runExportRequest: %v", err)
	}
	if all.scope != "all" || all.maxRows != 250_000 {
		t.Fatalf("all request = %+v, want scope=all bounded at 250000", all)
	}
	if mode := all.pageRequest().Mode(); mode != query.PagingCursor {
		t.Fatalf("an --all walk pages by %s, want cursor", mode)
	}
}
