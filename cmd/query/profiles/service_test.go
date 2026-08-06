package profiles

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

// Every concrete backend carries its own vendor mark, so a sidebar of profiles
// is scannable by provider at a glance. Only `sql` (a family, not a product)
// and unknown types fall back to a generic glyph.
func TestProviderIcon(t *testing.T) {
	cases := map[string]string{
		"sql":           "database",
		"postgres":      "postgres",
		"mysql":         "mysql",
		"sqlserver":     "sqlserver",
		"clickhouse":    "clickhouse",
		"http":          "globe",
		"postgrest":     "globe",
		"prometheus":    "prometheus",
		"loki":          "grafana",
		"opensearch":    "opensearch",
		"opentelemetry": "opentelemetry",
		"jaeger":        "activity",
		"":              "table",
		"unknown":       "table",
	}
	for providerType, want := range cases {
		if got := providerIcon(providerType); got != want {
			t.Errorf("providerIcon(%q) = %q, want %q", providerType, got, want)
		}
	}
}

// An explicit `icon:` is how a profile says what it is *about* when that differs
// from the backend it happens to read.
func TestProfileIconPrefersTheExplicitOverride(t *testing.T) {
	profile := sampleProfile("Orders")
	profile.Provider.Type = "postgres"
	if got := profileIcon(profile); got != "postgres" {
		t.Errorf("profileIcon without an override = %q, want postgres", got)
	}

	profile.Icon = "kubernetes"
	if got := profileIcon(profile); got != "kubernetes" {
		t.Errorf("profileIcon with an override = %q, want kubernetes", got)
	}
}

// The hierarchy is derived from the name and nothing else — the slug flattens
// every separator to "-", so it cannot be recovered from the surface key.
func TestProfileSurfacePath(t *testing.T) {
	cases := map[string]string{
		"jms":                        "jms",
		"jms.incoming":               "jms/incoming",
		"jms.incoming.disbursements": "jms/incoming/disbursements",
		"logs/api":                   "logs/api",
		// A hyphen is an ordinary name character: `remote-debugger` is one
		// segment, not two.
		"remote-debugger.sql-xevent": "remote-debugger/sql-xevent",
		"SQL Users":                  "SQL Users",
		"":                           "",
	}
	for name, want := range cases {
		if got := profileSurfacePath(name); got != want {
			t.Errorf("profileSurfacePath(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestProfileItemTableProvider(t *testing.T) {
	p := profileItem{sampleProfile("Sales Report")}

	cols := p.Columns()
	wantOrder := []string{"name", "type", "connection", "query"}
	if len(cols) != len(wantOrder) {
		t.Fatalf("got %d columns, want %d", len(cols), len(wantOrder))
	}
	for i, name := range wantOrder {
		if cols[i].Name != name {
			t.Errorf("column %d = %q, want %q", i, cols[i].Name, name)
		}
	}

	row := p.Row()
	if row["name"] != "Sales Report" {
		t.Errorf("row name = %v, want Sales Report", row["name"])
	}
	if row["type"] != "sql" {
		t.Errorf("row type = %v, want sql (the connection type)", row["type"])
	}
	if row["connection"] != "connection://db" {
		t.Errorf("row connection = %v, want connection://db", row["connection"])
	}
}

func TestProfileEntitySchema(t *testing.T) {
	p := sampleProfile("Sales Report") // sql provider, one "id" column
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatalf("profileEntitySchema: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if doc["x-clicky-parent"] != profileSurfaceParent {
		t.Errorf("x-clicky-parent = %v, want %q", doc["x-clicky-parent"], profileSurfaceParent)
	}
	if doc["x-clicky-icon"] != "database" {
		t.Errorf("x-clicky-icon = %v, want database", doc["x-clicky-icon"])
	}
	if doc["x-clicky-title"] != "Sales Report" {
		t.Errorf("x-clicky-title = %v, want Sales Report", doc["x-clicky-title"])
	}

	props, _ := doc["properties"].(map[string]any)
	idProp, _ := props["id"].(map[string]any)
	if idProp == nil || idProp["x-clicky-id"] != true {
		t.Errorf("the id column must be marked x-clicky-id, got %v", props["id"])
	}

	if _, ok := doc["x-clicky-render"]; ok {
		t.Errorf("a profile with no render mode must not emit x-clicky-render, got %v", doc["x-clicky-render"])
	}
}

func TestProfileEntitySchemaEmitsRenderMode(t *testing.T) {
	p := sampleProfile("Jaeger Traces")
	p.Render = query.RenderLogs
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatalf("profileEntitySchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if doc["x-clicky-render"] != "logs" {
		t.Errorf("x-clicky-render = %v, want logs", doc["x-clicky-render"])
	}
}

func TestProfileEntitySchemaEmitsColumnUnit(t *testing.T) {
	p := sampleProfile("Metrics")
	p.Columns = []query.ColumnDef{{Name: "ratio", Type: query.ColumnTypeNumber, Format: "float", Unit: "percentunit"}}
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ratio := doc["properties"].(map[string]any)["ratio"].(map[string]any)
	if ratio["x-clicky-format"] != "float" || ratio["x-clicky-unit"] != "percentunit" {
		t.Fatalf("ratio display metadata = %#v", ratio)
	}
}

func TestProfileEntitySchemaBindsFilterableOpenSearchColumns(t *testing.T) {
	p := sampleProfile("Searchable")
	p.Provider.Type = "opensearch"
	p.Columns = []query.ColumnDef{
		{Name: "service"},
		{Name: "status", CEL: `jsonpath("$.status", row.payload)`, Filter: &query.ColumnFilterDef{Field: "payload.status"}},
		{Name: "unmapped", CEL: `jsonpath("$.name", row.payload)`},
	}
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	for _, name := range []string{"service", "status"} {
		property := props[name].(map[string]any)
		if property["x-clicky-filter"] == nil || property["x-clicky-filter-key"] != "filter."+name {
			t.Fatalf("column %q filter metadata = %#v", name, property)
		}
	}
	if _, exists := props["unmapped"].(map[string]any)["x-clicky-filter"]; exists {
		t.Fatalf("complex CEL without an override must not be filterable: %#v", props["unmapped"])
	}
}

func TestFilterLookupParamsKeepProfileAndColumnFiltersOnly(t *testing.T) {
	got := filterLookupParams(query.Profile{Params: []query.ParamDef{{Name: "region"}}}, map[string]string{
		"region": "prod", "filter.service": "api", "__lookup_q": "pay", "offset": "100",
	})
	if len(got) != 2 || got["region"] != "prod" || got["filter.service"] != "api" {
		t.Fatalf("lookup params = %#v", got)
	}
}

func TestProfileEntitySchemaStructuredColumnShapes(t *testing.T) {
	p := sampleProfile("Structured")
	p.Columns = []query.ColumnDef{
		{Name: "labels", Type: query.ColumnTypeKeyValue},
		{Name: "pairs", Type: query.ColumnTypeKeyValues},
		{Name: "metadata", Type: query.ColumnTypeJSON},
	}
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	labels := props["labels"].(map[string]any)
	if labels["type"] != "object" || labels["x-clicky-type"] != "key_value" {
		t.Fatalf("labels schema = %#v", labels)
	}
	pairs := props["pairs"].(map[string]any)
	if pairs["type"] != "array" || pairs["x-clicky-type"] != "key_values" {
		t.Fatalf("pairs schema = %#v", pairs)
	}
	metadata := props["metadata"].(map[string]any)
	if _, ok := metadata["oneOf"].([]any); !ok || metadata["x-clicky-type"] != "json" {
		t.Fatalf("metadata schema = %#v", metadata)
	}
}

func TestProfileEntitySchemaSynthesizesIDWhenNoColumns(t *testing.T) {
	p := query.Profile{Name: "No Cols", Provider: query.ProviderConfig{Type: "http"}}
	raw, err := profileEntitySchema(p)
	if err != nil {
		t.Fatalf("profileEntitySchema: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, _ := doc["properties"].(map[string]any)
	idProp, _ := props["id"].(map[string]any)
	if idProp == nil || idProp["x-clicky-id"] != true {
		t.Errorf("a column-less profile must synthesize an x-clicky-id property, got %v", props)
	}
	if doc["x-clicky-icon"] != "globe" {
		t.Errorf("x-clicky-icon = %v, want globe", doc["x-clicky-icon"])
	}
}

// Registration is what tells the browser which control a filter renders as, and
// it happens once at startup — so a column kind that cannot be registered takes
// the whole server down rather than degrading one surface. Every kind must
// register, including the ones with no values to offer.
func TestRegisterProfileEntitiesRegistersEveryFilterKind(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	const profileName = "Filter Kind Probe"
	profile := sampleProfile(profileName)
	profile.Columns = []query.ColumnDef{
		{Name: "region", Type: query.ColumnTypeString},
		{Name: "latency_ms", Type: query.ColumnTypeNumber},
		{Name: "created_at", Type: query.ColumnTypeDateTime},
		{Name: "deleted", Type: query.ColumnTypeBoolean},
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatalf("Save: %v", err)
	}
	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.RegisterDynamic(context.Background()); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}

	for column, want := range map[string]string{
		"region": "multi-filter", "latency_ms": "number", "created_at": "date", "deleted": "bool",
	} {
		registered, ok := entity.GetFilter(profileFilterName(profileName, column))
		if !ok {
			t.Fatalf("column %q registered no filter", column)
		}
		if registered.Type != want {
			t.Errorf("column %q filter type = %q, want %q", column, registered.Type, want)
		}
		if want == "multi-filter" {
			continue
		}
		// A typed control is filled in rather than chosen from, so its option set
		// is empty — but it still has to answer, because the registry has no way
		// to express a filter that cannot be asked. It must answer without
		// reaching the backend: there is no list there to go and read.
		options, total, err := registered.Source.Options(entity.FilterContext{}, "", 0)
		if err != nil {
			t.Errorf("column %q options: %v", column, err)
		}
		if len(options) != 0 || total != 0 {
			t.Errorf("column %q offers %d of %d options, want none", column, len(options), total)
		}
	}
}

// TestRegisterProfileEntitiesEmitsSurfaceWithIcon exercises the real clicky path:
// the generated schema must be accepted by the dynamic-entity parser and produce
// an OpenAPI surface carrying the provider icon and the profile name as title.
func TestRegisterProfileEntitiesEmitsSurfaceWithIcon(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	const profileName = "Surface Icon Probe"
	if err := store.Save(context.Background(), sampleProfile(profileName)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.RegisterDynamic(context.Background()); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}

	root := &cobra.Command{Use: "query"}
	clicky.GenerateCLI(root)
	spec, err := rpc.NewOpenAPIGenerator(nil).GenerateFromCobra(root)
	if err != nil {
		t.Fatalf("GenerateFromCobra: %v", err)
	}
	if spec.Clicky == nil {
		t.Fatal("spec carries no x-clicky surfaces")
	}

	const want = "profile-surface-icon-probe"
	var found *rpc.ClickySurface
	for i := range spec.Clicky.Surfaces {
		if spec.Clicky.Surfaces[i].Entity == want {
			found = &spec.Clicky.Surfaces[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no surface for entity %q in %d surfaces", want, len(spec.Clicky.Surfaces))
	}
	if found.Icon != "database" {
		t.Errorf("surface icon = %q, want database", found.Icon)
	}
	if found.Parent != profileSurfaceParent {
		t.Errorf("surface parent = %q, want %q", found.Parent, profileSurfaceParent)
	}
	if found.Title != profileName {
		t.Errorf("surface title = %q, want %q", found.Title, profileName)
	}
	// The title stays the full name for page headings; the path is what nests
	// the sidebar. A name with no separator is a single root-level segment.
	if found.Path != profileName {
		t.Errorf("surface path = %q, want %q", found.Path, profileName)
	}
}

// TestRegisterProfileEntitiesEmitsSurfacePath is the sidebar-hierarchy contract:
// a dotted profile name must reach the frontend as a "/"-separated x-clicky-path,
// because the surface key ("profile-jms-incoming") has already flattened every
// separator to a hyphen and cannot be un-flattened.
func TestRegisterProfileEntitiesEmitsSurfacePath(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	const profileName = "surfacepath.incoming.disbursements"
	if err := store.Save(context.Background(), sampleProfile(profileName)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.RegisterDynamic(context.Background()); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}

	root := &cobra.Command{Use: "query"}
	clicky.GenerateCLI(root)
	spec, err := rpc.NewOpenAPIGenerator(nil).GenerateFromCobra(root)
	if err != nil {
		t.Fatalf("GenerateFromCobra: %v", err)
	}
	if spec.Clicky == nil {
		t.Fatal("spec carries no x-clicky surfaces")
	}

	const want = "profile-surfacepath-incoming-disbursements"
	for i := range spec.Clicky.Surfaces {
		if spec.Clicky.Surfaces[i].Entity != want {
			continue
		}
		if got := spec.Clicky.Surfaces[i].Path; got != "surfacepath/incoming/disbursements" {
			t.Errorf("surface path = %q, want surfacepath/incoming/disbursements", got)
		}
		return
	}
	t.Fatalf("no surface for entity %q in %d surfaces", want, len(spec.Clicky.Surfaces))
}

func TestRegisterProfileEntityColumnFiltersIdempotently(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	profile := sampleProfile("Filter Registration Probe")
	profile.Provider.Type = "opensearch"
	profile.Columns = []query.ColumnDef{{Name: "service"}}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatalf("Save: %v", err)
	}

	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for range 2 {
		if err := service.RegisterDynamic(context.Background()); err != nil {
			t.Fatalf("RegisterDynamic: %v", err)
		}
	}

	if filter, ok := entity.GetFilter(profileFilterName(profile.Name, "service")); !ok || filter.Source == nil {
		t.Fatalf("registered profile filter = %#v, exists = %v", filter, ok)
	}
}

// A bound list param has no schema property, so it reaches the browser only if
// the dynamic entity offers it by key.
func TestRegisterProfileEntityOffersBoundListParamsAsFilters(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	profile := sampleProfile("Param Filter Probe")
	profile.Provider.Type = "opensearch"
	profile.Columns = []query.ColumnDef{{Name: "service"}}
	profile.Params = []query.ParamDef{
		{Name: "regions", Label: "Regions", Type: query.ParamTypeList, Field: "region.keyword"},
		{Name: "plain", Type: query.ParamTypeList},
	}
	if err := store.Save(context.Background(), profile); err != nil {
		t.Fatalf("Save: %v", err)
	}

	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := service.RegisterDynamic(context.Background()); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}

	filter, ok := entity.GetFilter(profileParamFilterName(profile.Name, "regions"))
	if !ok || filter.Source == nil {
		t.Fatalf("bound list param filter = %#v, exists = %v", filter, ok)
	}
	if filter.Label != "Regions" || filter.Type != "multi-filter" || !filter.Multi {
		t.Fatalf("param filter should be a labelled multi-filter: %#v", filter)
	}
	// An unbound list can carry no exclusion, so it gets no filter at all.
	if _, exists := entity.GetFilter(profileParamFilterName(profile.Name, "plain")); exists {
		t.Fatal("an unbound list param should not register a filter")
	}

	// Registering the filter is not enough: the entity has to offer it, keyed by
	// the param name the request actually sends.
	info, ok := entity.GetEntity("profile-" + slugify(profile.Name))
	if !ok {
		t.Fatal("dynamic profile entity was not registered")
	}
	if got := info.FilterRefs["regions"]; got != profileParamFilterName(profile.Name, "regions") {
		t.Fatalf("entity does not offer the param filter: FilterRefs = %#v", info.FilterRefs)
	}
	if _, offered := info.FilterRefs["plain"]; offered {
		t.Fatalf("an unbound list param should not be offered: FilterRefs = %#v", info.FilterRefs)
	}
	if _, offered := info.FilterRefs["filter.service"]; !offered {
		t.Fatalf("the column filter should still be offered: FilterRefs = %#v", info.FilterRefs)
	}
}
