package profiles

import (
	"context"
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

// A filter one side does not declare must not fail the whole run — the two
// backends rarely accept the same parameters.
func TestReconcileRoutesParamsToTheSideThatDeclaresThem(t *testing.T) {
	source := sideProfile(t, "params-source", "recon-source-params", "",
		[]query.ParamDef{{Name: "policy", Type: query.ParamTypeString}}, []query.Row{{"id": "A"}})
	dest := sideProfile(t, "params-dest", "recon-dest-params", "", nil, []query.Row{{"id": "A"}})
	service := serviceOver(t, source, dest)

	result, err := service.Reconcile(context.Background(), source.Name, ReconcileFlags{
		Dest: dest.Name, KeyColumns: []string{"id"}, Params: []string{"policy=POL1"},
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
		"no dest":       {ReconcileFlags{KeyColumns: []string{"id"}}, "--dest is required"},
		"unknown dest":  {ReconcileFlags{Dest: "nope", KeyColumns: []string{"id"}}, `profile "nope" not found`},
		"no key":        {ReconcileFlags{Dest: dest.Name}, "columns or a cel expression"},
		"both key sets": {ReconcileFlags{Dest: dest.Name, KeyColumns: []string{"id"}, KeyCEL: `row.id`}, "pick one"},
		"bad param":     {ReconcileFlags{Dest: dest.Name, KeyColumns: []string{"id"}, Params: []string{"noequals"}}, "expected key=value"},
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
