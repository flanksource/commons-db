package profiles

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

func TestProviderIcon(t *testing.T) {
	cases := map[string]string{
		"sql":        "database",
		"postgres":   "database",
		"clickhouse": "database",
		"http":       "globe",
		"postgrest":  "globe",
		"prometheus": "graph",
		"loki":       "activity",
		"opensearch": "globe",
		"":           "table",
		"unknown":    "table",
	}
	for providerType, want := range cases {
		if got := providerIcon(providerType); got != want {
			t.Errorf("providerIcon(%q) = %q, want %q", providerType, got, want)
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
}
