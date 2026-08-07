package profiles

import (
	"context"
	"testing"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
)

func TestMergeStoredProfilesTracksStoreImmediately(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := sampleProfile("Live Sales")
	profile.Provider.Type = "postgres"
	profile.Params = []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}, Required: true}}
	profile.Columns = []query.ColumnDef{{Name: "id", Label: "ID", Type: query.ColumnTypeNumber, Kind: query.ColumnKindTimestamp, Format: "float", Unit: "short"}}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatal(err)
	}

	spec := &rpc.OpenAPISpec{
		Paths: map[string]rpc.OpenAPIPath{
			"/api/v1/profile-stale": {"get": {Clicky: &rpc.ClickyOperationMeta{Surface: "profile-stale"}}},
		},
		Clicky: &rpc.ClickySpecMeta{Surfaces: []rpc.ClickySurface{
			{Key: "profiles", Entity: "profiles", Title: "Profiles"},
			{Key: "profile-stale", Entity: "profile-stale", Parent: profileSurfaceParent},
		}},
	}
	if err := mergeStoredProfiles(spec, store); err != nil {
		t.Fatal(err)
	}
	if _, exists := spec.Paths["/api/v1/profile-stale"]; exists {
		t.Fatal("startup-snapshotted profile path was not removed")
	}
	op, exists := spec.Paths["/api/v1/profile/profile-live-sales"]["get"]
	if !exists {
		t.Fatalf("live profile operation missing: %+v", spec.Paths)
	}
	if op.Clicky == nil || op.Clicky.Surface != "profile-live-sales" || op.Clicky.Scope != "collection" {
		t.Fatalf("unexpected clicky metadata: %+v", op.Clicky)
	}
	if !op.Parameters[0].Required || op.Parameters[0].Clicky.Role != "filter" {
		t.Fatalf("profile filter parameter missing: %+v", op.Parameters)
	}
	// Asserted by role rather than by position: the parameter list grows with
	// every filterable column, and which paging roles are offered is the fact
	// under test.
	roles := map[string]bool{}
	for _, param := range op.Parameters {
		roles[string(param.Clicky.Role)] = true
	}
	if !roles["limit"] {
		t.Fatalf("limit is valid without an order and must be offered: %+v", op.Parameters)
	}
	// This profile declares no order, so there is no position either a cursor or
	// an offset could name, and neither is offered. Advertising one would put a
	// pager in the UI whose every click is a 400.
	if roles["cursor"] || roles["offset"] {
		t.Fatalf("position parameter offered on a profile that declares no order: %+v", op.Parameters)
	}
	if got := op.Parameters[0].Schema.Enum; len(got) != 2 || got[0] != "US" {
		t.Fatalf("profile enum missing: %+v", got)
	}
	item := op.Responses["200"].Content["application/json"].Schema.Items
	if item.Properties["id"].Extensions["x-clicky-label"] != "ID" {
		t.Fatalf("response column metadata missing: %+v", item.Properties["id"])
	}
	if item.Properties["id"].Extensions["x-clicky-kind"] != "timestamp" {
		t.Fatalf("response timestamp metadata missing: %+v", item.Properties["id"])
	}
	if item.Properties["id"].Extensions["x-clicky-format"] != "float" || item.Properties["id"].Extensions["x-clicky-unit"] != "short" {
		t.Fatalf("response display metadata missing: %+v", item.Properties["id"])
	}
	if op.Clicky.Export == nil || len(op.Clicky.Export.Formats) != 8 || len(op.Clicky.Export.Scopes) != 2 || op.Clicky.Export.AllRowsMode != "streaming" || op.Clicky.Export.FormatMaxRows["pdf"] != 1000 {
		t.Fatalf("profile export metadata missing: %+v", op.Clicky.Export)
	}

	if err := store.Delete(context.Background(), profile.Name); err != nil {
		t.Fatal(err)
	}
	if err := mergeStoredProfiles(spec, store); err != nil {
		t.Fatal(err)
	}
	if _, exists := spec.Paths["/api/v1/profile/profile-live-sales"]; exists {
		t.Fatal("deleted profile remained in dynamic OpenAPI")
	}
}

func TestProfileOpenAPIPreservesMappedPagingAndTimeRoles(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	if err := addProfileToSpec(spec, query.Profile{
		Name: "mapped",
		Params: []query.ParamDef{
			{Name: "page_size", Type: query.ParamTypeNumber, Role: query.ParamRoleLimit},
			{Name: "skip", Type: query.ParamTypeNumber, Role: query.ParamRoleOffset},
			{Name: "from", Type: query.ParamTypeDate, Role: query.ParamRoleTimeFrom},
			{Name: "to", Type: query.ParamTypeDate, Role: query.ParamRoleTimeTo},
		},
	}); err != nil {
		t.Fatal(err)
	}
	op := spec.Paths["/api/v1/profile/profile-mapped"]["get"]
	if len(op.Parameters) != 4 {
		t.Fatalf("mapped parameters should replace built-in pager names: %+v", op.Parameters)
	}
	wantRoles := []string{"limit", "offset", "time-from", "time-to"}
	for i, role := range wantRoles {
		if op.Parameters[i].Clicky == nil || op.Parameters[i].Clicky.Role != role {
			t.Fatalf("parameter %d role = %+v, want %q", i, op.Parameters[i].Clicky, role)
		}
	}
}

func TestProfileOpenAPIAdvertisesStructuredColumnShapes(t *testing.T) {
	schema := profileResponseSchema(query.Profile{Columns: []query.ColumnDef{
		{Name: "labels", Type: query.ColumnTypeKeyValue},
		{Name: "pairs", Type: query.ColumnTypeKeyValues},
		{Name: "metadata", Type: query.ColumnTypeJSON},
	}}, nil).Items

	labels := schema.Properties["labels"]
	if labels.Type != "object" || labels.AdditionalProperties == nil || labels.Extensions["x-clicky-type"] != "key_value" {
		t.Fatalf("labels schema = %#v", labels)
	}
	pairs := schema.Properties["pairs"]
	if pairs.Type != "array" || pairs.Items == nil || pairs.Items.Properties["key"].Type != "string" {
		t.Fatalf("pairs schema = %#v", pairs)
	}
	metadata := schema.Properties["metadata"]
	if len(metadata.OneOf) != 5 || !metadata.Nullable || metadata.Extensions["x-clicky-type"] != "json" {
		t.Fatalf("metadata schema = %#v", metadata)
	}
}

func TestProfileOpenAPIAdvertisesNativeColumnFilters(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	if err := addProfileToSpec(spec, query.Profile{
		Name:     "logs",
		Provider: query.ProviderConfig{Type: "opensearch"},
		Columns: []query.ColumnDef{
			{Name: "service", Label: "Service"},
			{Name: "payload.user", CEL: `jsonpath("$.user", row.payload)`, Filter: &query.ColumnFilterDef{Field: "payload.user.keyword"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Two column filters plus limit. This profile declares no order, so no
	// offset or cursor is advertised.
	op := spec.Paths["/api/v1/profile/profile-logs"]["get"]
	if len(op.Parameters) != 3 {
		t.Fatalf("column filters plus pagination missing: %+v", op.Parameters)
	}
	for i, key := range []string{"filter.service", "filter.payload.user"} {
		parameter := op.Parameters[i]
		if parameter.Name != key || parameter.Clicky == nil || parameter.Clicky.Role != "filter" || parameter.Lookup == nil || parameter.Lookup.Filter != key || !parameter.Lookup.Multi {
			t.Fatalf("parameter %q = %+v", key, parameter)
		}
		if spec.Components == nil || spec.Components.ClickyFilters == nil {
			t.Fatalf("filter component for %q is missing", key)
		}
	}
	item := op.Responses["200"].Content["application/json"].Schema.Items
	if item.Properties["service"].Extensions["x-clicky-filter-key"] != "filter.service" {
		t.Fatalf("response filter key missing: %+v", item.Properties["service"])
	}
}

// The control the browser renders comes from the column's kind, and a filter
// whose values cannot be enumerated must not advertise a lookup URL — pointing
// one at a range would offer a list that has no answer.
func TestProfileOpenAPIAdvertisesEachFilterKindsOwnControl(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	if err := addProfileToSpec(spec, query.Profile{
		Name:     "orders",
		Provider: query.ProviderConfig{Type: "postgres"},
		Columns: []query.ColumnDef{
			{Name: "region", Type: query.ColumnTypeString},
			{Name: "latency_ms", Type: query.ColumnTypeNumber},
			{Name: "created_at", Type: query.ColumnTypeDateTime},
			{Name: "deleted", Type: query.ColumnTypeBoolean},
			{Name: "env", Filter: &query.ColumnFilterDef{Options: []string{"prod", "dev"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	op := spec.Paths["/api/v1/profile/profile-orders"]["get"]
	byName := map[string]rpc.OpenAPIParameter{}
	for _, parameter := range op.Parameters {
		byName[parameter.Name] = parameter
	}
	for key, wantLookup := range map[string]bool{
		"filter.region":     true,
		"filter.latency_ms": false,
		"filter.created_at": false,
		"filter.deleted":    false,
		// Enumerated values are already the answer, so there is nothing to fetch.
		"filter.env": false,
	} {
		parameter, ok := byName[key]
		if !ok {
			t.Fatalf("parameter %q is missing from %+v", key, op.Parameters)
		}
		if (parameter.Lookup != nil) != wantLookup {
			t.Fatalf("parameter %q lookup = %+v, want present=%v", key, parameter.Lookup, wantLookup)
		}
	}
	if got := byName["filter.env"].Schema.Enum; len(got) != 2 || got[0] != "prod" {
		t.Fatalf("enumerated filter options missing: %+v", got)
	}

	filters := spec.Components.ClickyFilters
	for name, want := range map[string]string{
		"region": "multi-filter", "latency_ms": "number", "created_at": "date-range", "deleted": "bool",
	} {
		filter, ok := filters[profileFilterName("orders", name)]
		if !ok {
			t.Fatalf("filter component for %q is missing", name)
		}
		if filter.Type != want {
			t.Fatalf("filter %q type = %q, want %q", name, filter.Type, want)
		}
	}
}

// A bound list param must reach the browser as the same multi-filter a native
// column filter does — that pairing is what renders the tri-state control.
func TestProfileOpenAPIAdvertisesBoundListParamsAsMultiFilters(t *testing.T) {
	spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
	if err := addProfileToSpec(spec, query.Profile{
		Name:     "logs",
		Provider: query.ProviderConfig{Type: "opensearch"},
		Params: []query.ParamDef{
			{Name: "regions", Type: query.ParamTypeList, Field: "region", Options: []string{"us-east", "eu"}},
			{Name: "accounts", Type: query.ParamTypeList, Field: "account_id"},
			{Name: "plain", Type: query.ParamTypeList},
		},
	}); err != nil {
		t.Fatal(err)
	}
	op := spec.Paths["/api/v1/profile/profile-logs"]["get"]

	byName := map[string]rpc.OpenAPIParameter{}
	for _, parameter := range op.Parameters {
		byName[parameter.Name] = parameter
	}

	// Static options are inlined, so the control needs no server round trip.
	static := byName["regions"]
	if static.Lookup == nil || !static.Lookup.Multi {
		t.Fatalf("static list param is not a multi lookup: %+v", static)
	}
	if static.Lookup.URL != "" || static.Lookup.SearchParam != "" {
		t.Fatalf("static options should not advertise a search endpoint: %+v", static.Lookup)
	}
	staticFilter := spec.Components.ClickyFilters[profileParamFilterName("logs", "regions")]
	if staticFilter.Type != "multi-filter" || !staticFilter.Multi {
		t.Fatalf("static filter component = %+v", staticFilter)
	}
	if staticFilter.Source.Kind != entity.SourceStatic || len(staticFilter.Source.Options) != 2 {
		t.Fatalf("static options missing from the component: %+v", staticFilter.Source)
	}

	// Without options the provider answers, so the lookup carries a search URL.
	dynamic := byName["accounts"]
	if dynamic.Lookup == nil || dynamic.Lookup.URL != "/api/v1/profile/profile-logs" || dynamic.Lookup.SearchParam != "__lookup_q" {
		t.Fatalf("dynamic list param lookup = %+v", dynamic.Lookup)
	}
	if got := spec.Components.ClickyFilters[profileParamFilterName("logs", "accounts")].Source.Kind; got != entity.SourceCustom {
		t.Fatalf("dynamic source kind = %q, want %q", got, entity.SourceCustom)
	}

	// An unbound list can hold no exclusion, so it stays a plain parameter.
	if byName["plain"].Lookup != nil {
		t.Fatalf("unbound list param should advertise no filter: %+v", byName["plain"])
	}
}

// A cursor is only offered by a profile that can serve one. A UI shown a cursor
// param enters cursor mode, so advertising it on a profile that has no total
// order to name a position in would put every page it then asks for into a
// refusal.
func TestProfileAdvertisesCursorOnlyWhenItCanServeOne(t *testing.T) {
	for _, tt := range []struct {
		name  string
		order query.Order
		want  bool
	}{
		{name: "ordered", order: query.Order{{Column: "created_at"}, {Column: "id", Unique: true}}, want: true},
		{name: "unordered", want: false},
		// An order with no unique tiebreaker is not a total order, so a
		// position in it names a set of rows rather than a row.
		{name: "no tiebreaker", order: query.Order{{Column: "created_at"}}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			profile := sampleProfile("Cursor Probe")
			profile.Provider.Type = "sql"
			profile.Order = tt.order
			if err := store.Save(context.Background(), profile); err != nil {
				t.Fatal(err)
			}

			spec := &rpc.OpenAPISpec{
				Paths:  map[string]rpc.OpenAPIPath{},
				Clicky: &rpc.ClickySpecMeta{Surfaces: []rpc.ClickySurface{{Key: "profiles", Entity: "profiles"}}},
			}
			if err := mergeStoredProfiles(spec, store); err != nil {
				t.Fatal(err)
			}
			op := spec.Paths["/api/v1/profile/profile-cursor-probe"]["get"]
			var offered bool
			for _, param := range op.Parameters {
				if param.Clicky != nil && param.Clicky.Role == "cursor" {
					offered = true
				}
			}
			if offered != tt.want {
				t.Fatalf("cursor offered = %v, want %v: %+v", offered, tt.want, op.Parameters)
			}
		})
	}
}

// A param and a column that share a name must not overwrite each other's filter.
func TestProfileParamAndColumnFilterNamesDoNotCollide(t *testing.T) {
	if profileParamFilterName("logs", "service") == profileFilterName("logs", "service") {
		t.Fatal("param and column filter names collide")
	}
}
