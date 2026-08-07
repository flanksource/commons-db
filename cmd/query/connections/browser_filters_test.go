package connections

import (
	"reflect"
	"testing"
	"time"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
)

func TestSQLColumnTypeReadsTheDriversDecoratedNames(t *testing.T) {
	// A driver reports a type decorated in ways that say nothing about how a
	// value compares — a length, an array marker, a timezone clause.
	for name, want := range map[string]query.ColumnType{
		"TEXT":                        query.ColumnTypeString,
		"VARCHAR(255)":                query.ColumnTypeString,
		"UUID":                        query.ColumnTypeUUID,
		"UNIQUEIDENTIFIER":            query.ColumnTypeUUID,
		"INT4":                        query.ColumnTypeNumber,
		"BIGINT UNSIGNED":             query.ColumnTypeNumber,
		"NUMERIC(10,2)":               query.ColumnTypeNumber,
		"TIMESTAMP WITH TIME ZONE":    query.ColumnTypeDateTime,
		"TIMESTAMPTZ":                 query.ColumnTypeDateTime,
		"BOOL":                        query.ColumnTypeBoolean,
		"_TEXT":                       query.ColumnTypeString,
		"TEXT[]":                      query.ColumnTypeString,
		"JSONB":                       "",
		"SOMETHING_NOBODY_HAS_MAPPED": "",
	} {
		if got := sqlColumnType(name); got != want {
			t.Errorf("sqlColumnType(%q) = %q, want %q", name, got, want)
		}
	}
}

// A driver names only the types it was compiled knowing, but it still says
// which Go type every column scans into — so a postgres enum, which arrives as
// a bare OID and is exactly the kind of column anyone wants to filter on, is
// still known to hold strings.
func TestSQLScanColumnTypeRecoversWhatTheTypeNameDidNotSay(t *testing.T) {
	for _, probe := range []struct {
		scanType reflect.Type
		want     query.ColumnType
	}{
		{reflect.TypeOf(""), query.ColumnTypeString},
		{reflect.TypeOf(true), query.ColumnTypeBoolean},
		{reflect.TypeOf(int64(0)), query.ColumnTypeNumber},
		{reflect.TypeOf(float64(0)), query.ColumnTypeNumber},
		{reflect.TypeOf(time.Time{}), query.ColumnTypeDateTime},
		// A driver that hands back "something" has said nothing about how two of
		// them compare.
		{reflect.TypeOf([]byte(nil)), ""},
		{reflect.TypeOf(map[string]any(nil)), ""},
		{reflect.TypeOf((*any)(nil)).Elem(), ""},
		{nil, ""},
	} {
		if got := sqlScanColumnType(probe.scanType); got != probe.want {
			t.Errorf("sqlScanColumnType(%v) = %q, want %q", probe.scanType, got, probe.want)
		}
	}
}

// A driver names only the types it was compiled knowing. A user-defined type —
// a postgres enum, which is exactly the kind of column anyone wants to filter
// on — arrives as a bare OID, and its name is worth nothing to a reader.
func TestSQLColumnTypeNameHidesAnOIDNobodyCanRead(t *testing.T) {
	for name, want := range map[string]string{
		"TEXT": "TEXT", "VARCHAR(255)": "VARCHAR(255)", "10840817": "", "": "", "  ": "",
	} {
		if got := sqlColumnTypeName(name); got != want {
			t.Errorf("sqlColumnTypeName(%q) = %q, want %q", name, got, want)
		}
	}
}

func browserSQLProfile(t *testing.T, columns []query.ColumnDef) query.Profile {
	t.Helper()
	descriptor, ok := descriptorForConnection(models.ConnectionTypePostgres)
	if !ok {
		t.Fatal("postgres has no browser descriptor")
	}
	conn := &models.Connection{ID: uuid.New(), Type: models.ConnectionTypePostgres}
	return browserProfile(descriptor, conn, "SELECT id, region FROM orders", nil, columns)
}

func TestBrowserColumnsOfferAFilterPerComparableColumn(t *testing.T) {
	profile := browserSQLProfile(t, []query.ColumnDef{
		{Name: "region", Type: query.ColumnTypeString},
		{Name: "latency_ms", Type: query.ColumnTypeNumber},
		{Name: "created_at", Type: query.ColumnTypeDateTime},
		{Name: "deleted", Type: query.ColumnTypeBoolean},
		// An unmapped type offers nothing rather than a filter that would mean
		// something other than what it says.
		{Name: "payload", Filter: &query.ColumnFilterDef{Disabled: true}},
	})

	columns, err := describeBrowserColumns(profile, map[string]string{"region": "TEXT"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, column := range columns {
		if column.Filter != nil {
			kinds[column.Name] = column.Filter.Kind
		}
	}
	want := map[string]string{
		"region": "terms", "latency_ms": "range", "created_at": "time", "deleted": "boolean",
	}
	if len(kinds) != len(want) {
		t.Fatalf("filter kinds = %+v, want %+v", kinds, want)
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("column %q filter kind = %q, want %q", name, kinds[name], kind)
		}
	}
	for _, column := range columns {
		if column.Name == "region" {
			if column.FilterKey != "filter.region" || column.DatabaseType != "TEXT" || !column.Filter.Lookup {
				t.Fatalf("region column = %+v", column)
			}
		}
		if column.Name == "payload" && column.Filter != nil {
			t.Fatalf("an unmapped column must offer no filter, got %+v", column.Filter)
		}
		// Only a value selection has a list to offer.
		if column.Name == "latency_ms" && column.Filter.Lookup {
			t.Fatal("a range filter must not advertise a value lookup")
		}
	}
}

// A value list over identifiers is a scan of the whole result that answers with
// a page of the rows. The column still filters — exactly, on values typed in.
func TestBrowserColumnsOfferNoValueListForIdentifiers(t *testing.T) {
	profile := browserSQLProfile(t, []query.ColumnDef{
		{Name: "id", Type: query.ColumnTypeUUID},
		{Name: "region", Type: query.ColumnTypeString},
	})

	columns, err := describeBrowserColumns(profile, map[string]string{"id": "UUID"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]browserColumn{}
	for _, column := range columns {
		byName[column.Name] = column
	}
	id := byName["id"]
	if id.Filter == nil || id.Filter.Kind != "terms" || id.Filter.Lookup {
		t.Fatalf("id column = %+v", id.Filter)
	}
	if id.FilterKey != "filter.id" {
		t.Fatalf("id filter key = %q", id.FilterKey)
	}
	if region := byName["region"]; region.Filter == nil || !region.Filter.Lookup {
		t.Fatalf("a string column still offers its values, got %+v", region.Filter)
	}
}

// The browser echoes the column set it was offered, and the echo is what a
// posted selection binds through. A filter it drops is a control the console
// offered and the server then declines to compile.
func TestBrowserColumnDefsRoundTripAnIdentifierFilter(t *testing.T) {
	profile := browserSQLProfile(t, []query.ColumnDef{{Name: "id", Type: query.ColumnTypeUUID}})
	echoed, err := describeBrowserColumns(profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	resolved := browserSQLProfile(t, browserColumnDefs(echoed))
	bindings, err := resolved.ColumnFilterBindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %+v", bindings)
	}
	if bindings[0].Kind != query.ColumnFilterKindTerms || bindings[0].Lookup {
		t.Fatalf("round-tripped binding = %+v", bindings[0])
	}
}

func TestResolveBrowserFiltersBindsToTheColumnsTheConsoleOffered(t *testing.T) {
	profile := browserSQLProfile(t, []query.ColumnDef{{Name: "region", Type: query.ColumnTypeString}})

	filters, err := resolveBrowserFilters(profile, map[string]string{"filter.region": "us-east,!eu"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 {
		t.Fatalf("filters = %+v", filters)
	}
	if filters[0].Field != "region" || filters[0].Kind != query.ColumnFilterKindTerms {
		t.Fatalf("filter = %+v", filters[0])
	}
	if len(filters[0].Include) != 1 || filters[0].Include[0] != "us-east" {
		t.Fatalf("include = %+v", filters[0].Include)
	}
	if len(filters[0].Exclude) != 1 || filters[0].Exclude[0] != "eu" {
		t.Fatalf("exclude = %+v", filters[0].Exclude)
	}
}

// A filter names a column, so one sent before the console has learned what the
// query returns has nothing to bind to and is refused rather than dropped.
func TestResolveBrowserFiltersRefusesAFilterWithNoColumns(t *testing.T) {
	profile := browserSQLProfile(t, nil)
	if _, err := resolveBrowserFilters(profile, map[string]string{"filter.region": "eu"}); err == nil {
		t.Fatal("expected a filter without columns to be refused")
	}
}

func TestResolveBrowserFiltersRefusesAnUnofferedColumn(t *testing.T) {
	profile := browserSQLProfile(t, []query.ColumnDef{{Name: "region", Type: query.ColumnTypeString}})
	if _, err := resolveBrowserFilters(profile, map[string]string{"filter.nope": "x"}); err == nil {
		t.Fatal("expected a filter on an unoffered column to be refused")
	}
}
