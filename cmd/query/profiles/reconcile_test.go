package profiles

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/query"
)

var reconcileEpoch = time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)

func columnNamesOf(result *query.ReconcileResult) []string {
	names := make([]string, 0, len(result.Columns()))
	for _, column := range result.Columns() {
		names = append(names, column.Name)
	}
	return names
}

// sideProfile registers a provider returning rows and returns a profile reading
// from it. timeColumn, when set, is declared as the profile's timestamp column.
func sideProfile(t *testing.T, name, providerType, timeColumn string, params []query.ParamDef, rows []query.Row) query.Profile {
	t.Helper()
	query.RegisterProvider(rowsProvider{name: providerType, rows: rows})
	profile := query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Params:   params,
		Columns:  []query.ColumnDef{{Name: "id"}},
	}
	if timeColumn != "" {
		profile.Columns = append(profile.Columns,
			query.ColumnDef{Name: timeColumn, Kind: query.ColumnKindTimestamp, Type: query.ColumnTypeDateTime})
	}
	return profile
}

func TestReconcileJoinsTwoProfilesOnACELKey(t *testing.T) {
	source := sideProfile(t, "outgoing", "recon-source-cel", "sent_at", nil, []query.Row{
		{"id": "A", "sent_at": reconcileEpoch},
		{"id": "B", "sent_at": reconcileEpoch},
	})
	dest := sideProfile(t, "incoming", "recon-dest-cel", "received_at", nil, []query.Row{
		{"id": "A", "received_at": reconcileEpoch.Add(2 * time.Second)},
		{"id": "C", "received_at": reconcileEpoch},
	})
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyCEL: `row.id`,
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	want := query.ReconcileStats{Matched: 1, OnlySource: 1, OnlyDest: 1}
	if result.Stats != want {
		t.Errorf("stats = %+v, want %+v", result.Stats, want)
	}
	if result.Source != source.Name || result.Dest != dest.Name {
		t.Errorf("sides = %q -> %q, want %q -> %q", result.Source, result.Dest, source.Name, dest.Name)
	}
	if diff := *result.Rows[0].TimeDiff; diff != 2*time.Second {
		t.Errorf("time diff = %s, want 2s", diff)
	}
}

func TestReconcileJoinsOnColumnNames(t *testing.T) {
	source := sideProfile(t, "left", "recon-source-cols", "", nil, []query.Row{{"id": "A"}})
	dest := sideProfile(t, "right", "recon-dest-cols", "", nil, []query.Row{{"id": "A"}, {"id": "B"}})
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyColumns: []string{"id"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if want := (query.ReconcileStats{Matched: 1, OnlyDest: 1}); result.Stats != want {
		t.Errorf("stats = %+v, want %+v", result.Stats, want)
	}
}

func TestReconcileAppliesAFilterToItsDeclaredSide(t *testing.T) {
	source := sideProfile(t, "params-source", "recon-source-params", "",
		[]query.ParamDef{{Name: "policy", Type: query.ParamTypeString}}, []query.Row{{"id": "A"}})
	dest := sideProfile(t, "params-dest", "recon-dest-params", "", nil, []query.Row{{"id": "A"}})
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyColumns: []string{"id"}, SourceFilters: []string{"policy=POL1"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Stats.Matched != 1 {
		t.Errorf("matched = %d, want 1", result.Stats.Matched)
	}
}

func TestReconcileRequiresBothSidesAndAKey(t *testing.T) {
	source := sideProfile(t, "guard-source", "recon-source-guard", "", nil, []query.Row{{"id": "A"}})
	dest := sideProfile(t, "guard-dest", "recon-dest-guard", "", nil, []query.Row{{"id": "A"}})
	service := serviceOver(t, source, dest)

	cases := map[string]struct {
		flags ReconcileFlags
		want  string
	}{
		"no dest":           {ReconcileFlags{KeyColumns: []string{"id"}}, "--dest is required"},
		"unknown dest":      {ReconcileFlags{Dest: "nope", KeyColumns: []string{"id"}}, `profile "nope" not found`},
		"no key":            {ReconcileFlags{Dest: dest.Name}, "columns or a cel expression"},
		"both key sets":     {ReconcileFlags{Dest: dest.Name, KeyColumns: []string{"id"}, KeyCEL: `row.id`}, "pick one"},
		"bad source filter": {ReconcileFlags{Dest: dest.Name, KeyColumns: []string{"id"}, SourceFilters: []string{"noequals"}}, "expected key=value"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := service.Reconcile(context.Background(), source.Name, tc.flags)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestReconcileRendersASideBySideTable(t *testing.T) {
	source := sideProfile(t, "render-source", "recon-source-render", "", nil, []query.Row{{"id": "A"}})
	dest := sideProfile(t, "render-dest", "recon-dest-render", "", nil, []query.Row{{"id": "A"}})
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyColumns: []string{"id"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	csv, err := result.Render("csv")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Each side's columns are labelled by side so the two "id" columns stay
	// tellable apart in the rendered table.
	for _, want := range []string{"src id", "dest id", "matched"} {
		if !strings.Contains(csv, want) {
			t.Errorf("csv is missing %q:\n%s", want, csv)
		}
	}
	if names := columnNamesOf(result); !slices.Contains(names, "render-source_id") || !slices.Contains(names, "render-dest_id") {
		t.Errorf("column keys = %v, want them namespaced per profile", names)
	}
	if summary := result.Pretty().Content; !strings.Contains(summary, "render-source -> render-dest") {
		t.Errorf("summary = %q", summary)
	}
}

// idRows builds count rows keyed id001, id002, … so a row bound is observable.
func idRows(count int) []query.Row {
	rows := make([]query.Row, 0, count)
	for i := 1; i <= count; i++ {
		rows = append(rows, query.Row{"id": fmt.Sprintf("id%03d", i)})
	}
	return rows
}

// A profile that stores a reconcile block runs with no flags at all: the stored
// block is the whole invocation.
func TestReconcileUsesTheBlockStoredOnTheProfile(t *testing.T) {
	dest := sideProfile(t, "stored-dest", "recon-dest-stored", "", nil, idRows(2))
	source := sideProfile(t, "stored-source", "recon-source-stored", "", nil, idRows(3))
	source.Reconcile = &query.ReconcileConfig{
		Dest: dest.Name,
		ReconcileSpec: query.ReconcileSpec{
			Range: &query.KeyRange{To: "id003"},
			Key:   query.KeySpec{Columns: []string{"id"}},
		},
	}
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Dest != dest.Name {
		t.Errorf("dest = %q, want %q", result.Dest, dest.Name)
	}
	if result.Config.Range == nil || result.Config.Range.To != "id003" {
		t.Errorf("range = %+v, want the stored range", result.Config.Range)
	}
	// The range covers id001 and id002, which both sides have. The source's
	// third row is outside the range rather than cut off inside it, so it is
	// not a finding — which a per-side row cap could not express.
	if want := (query.ReconcileStats{Matched: 2}); result.Stats != want {
		t.Errorf("stats = %+v, want %+v", result.Stats, want)
	}
	if result.Bounded() {
		t.Errorf("a ranged run reads both sides in full inside the range, so nothing was cut short")
	}
}

func TestReconcileFlagsOverrideTheStoredBlockFieldByField(t *testing.T) {
	stored := sideProfile(t, "override-stored-dest", "recon-dest-stored-override", "", nil, idRows(4))
	other := sideProfile(t, "override-other-dest", "recon-dest-other", "", nil, idRows(4))
	source := sideProfile(t, "override-source", "recon-source-override", "",
		[]query.ParamDef{{Name: "region", Type: query.ParamTypeString}, {Name: "tier", Type: query.ParamTypeString}}, idRows(4))
	source.Reconcile = &query.ReconcileConfig{
		Dest:          stored.Name,
		SourceFilters: map[string]string{"region": "eu", "tier": "gold"},
		ReconcileSpec: query.ReconcileSpec{
			Range: &query.KeyRange{To: "id002"},
			Key:   query.KeySpec{Columns: []string{"id"}},
		},
	}
	service := serviceOver(t, source, stored, other)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: other.Name, KeyTo: "id004", SourceFilters: []string{"region=us"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Dest != other.Name {
		t.Errorf("dest = %q, want the flag's %q", result.Dest, other.Name)
	}
	if result.Config.Range == nil || result.Config.Range.To != "id004" {
		t.Errorf("range = %+v, want the flag's id004", result.Config.Range)
	}
	// The key came from the stored block, which no flag overrode.
	if want := (query.ReconcileStats{Matched: 3}); result.Stats != want {
		t.Errorf("stats = %+v, want %+v", result.Stats, want)
	}
}

func TestReconcileMergesStoredSourceFiltersWithFlagFilters(t *testing.T) {
	dest := sideProfile(t, "merge-dest", "recon-dest-merge", "", nil, idRows(1))
	source := sideProfile(t, "merge-source", "recon-source-merge", "",
		[]query.ParamDef{{Name: "region", Type: query.ParamTypeString}, {Name: "tier", Type: query.ParamTypeString}},
		idRows(1))
	source.Reconcile = &query.ReconcileConfig{
		Dest:          dest.Name,
		SourceFilters: map[string]string{"region": "eu", "tier": "gold"},
		ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"id"}}},
	}
	service := serviceOver(t, source, dest)

	// Overriding one stored filter must not drop the others.
	config, err := reconcileConfig(source, ReconcileFlags{SourceFilters: []string{"region=us"}}, map[string]string{"region": "us"}, nil)
	if err != nil {
		t.Fatalf("reconcileConfig: %v", err)
	}
	if config.SourceFilters["region"] != "us" || config.SourceFilters["tier"] != "gold" {
		t.Errorf("source filters = %v, want region overridden and tier kept", config.SourceFilters)
	}
	if _, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{SourceFilters: []string{"region=us"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestReconcileRejectsARangeThatCoversNoKeys(t *testing.T) {
	dest := sideProfile(t, "range-dest", "recon-dest-range", "", nil, idRows(1))
	source := sideProfile(t, "range-source", "recon-source-range", "", nil, idRows(1))
	service := serviceOver(t, source, dest)

	_, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyColumns: []string{"id"}, KeyFrom: "id009", KeyTo: "id001",
	})
	if err == nil || !strings.Contains(err.Error(), "covers no keys") {
		t.Fatalf("err = %v, want it to reject an empty range", err)
	}
}
