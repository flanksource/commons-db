package profiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/query"
	"sigs.k8s.io/yaml"
)

func sampleProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "sql", Connection: "connection://db"},
		Query:    "select * from a where region = '{{.params.region}}'",
		Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}, Required: true}},
		Columns:  []query.ColumnDef{{Name: "id", Type: query.ColumnTypeString}},
	}
}

func TestProfileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}

	want := sampleProfile("Sales Report")
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(context.Background(), "Sales Report")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != want.Name || got.Query != want.Query {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if len(got.Params) != 1 || got.Params[0].Name != "region" || !got.Params[0].Required {
		t.Fatalf("params not preserved: %+v", got.Params)
	}
	if len(got.Columns) != 1 || got.Columns[0].Name != "id" {
		t.Fatalf("columns not preserved: %+v", got.Columns)
	}
	bySlug, err := store.Get(context.Background(), "profile-sales-report")
	if err != nil || bySlug.Name != want.Name {
		t.Fatalf("profile slug lookup failed: %+v err=%v", bySlug, err)
	}
	info, err := os.Stat(filepath.Join(store.Dir, "sales-report.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("profile file mode = %o, want 600", gotMode)
	}
}

func TestProfileStoreListAndDelete(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	for _, n := range []string{"Beta", "Alpha"} {
		if err := store.Save(context.Background(), sampleProfile(n)); err != nil {
			t.Fatalf("Save %q: %v", n, err)
		}
	}

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Beta" {
		t.Fatalf("expected [Alpha Beta] sorted, got %v", names(list))
	}

	if err := store.Delete(context.Background(), "Alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = store.List(context.Background())
	if len(list) != 1 || list[0].Name != "Beta" {
		t.Fatalf("expected [Beta] after delete, got %v", names(list))
	}
}

func TestProfileStoreMigratesLegacyTraceProfilesOnStartup(t *testing.T) {
	store, legacy := migratedActivityProfileStore(t)
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := names(list); len(got) != 2 || got[0] != "Current Profile" || got[1] != "activity.client" {
		t.Fatalf("expected migrated and current query profiles, got %v", got)
	}
	migrated := list[1]
	if migrated.Provider.Type != "legacy-trace" || migrated.Provider.Options["kind"] != "sql" {
		t.Fatalf("unexpected migrated provider: %+v", migrated.Provider)
	}
	if source, ok := migrated.Provider.Options["source"].(string); !ok || source != string(legacy) {
		t.Fatalf("legacy source was not preserved: %#v", migrated.Provider.Options["source"])
	}
	if len(migrated.Params) != 1 || migrated.Params[0].Name != "activity" || !migrated.Params[0].Required {
		t.Fatalf("legacy params were not converted: %+v", migrated.Params)
	}
	if len(migrated.Columns) != 1 || migrated.Columns[0].Name != "ClientGUID" || migrated.Columns[0].CEL != "span.ClientGUID" {
		t.Fatalf("legacy columns were not converted: %+v", migrated.Columns)
	}
}

func TestProfileStoreUpgradesInterimTraceImportsToExecutableProfiles(t *testing.T) {
	dir := t.TempDir()
	jaeger := []byte(`profile: jaeger
provider:
  type: legacy-trace
  options:
    kind: opensearch
    source: |
      name: jaeger
      format: jaeger
      index: jaeger-span*
      params:
        namespace:
          field: process.serviceName
          operator: term
`)
	jms := []byte(`profile: jms
provider:
  type: legacy-trace
  options:
    kind: import
    source: |
      name: jms
      imports: [jaeger]
      params:
        namespace:
          field: process.serviceName
          template: "{value}-api"
      aliases:
        request.xml:
          cel: span["input.xml"]
        request.copy:
          cel: request.xml
      ignore: [input.xml]
`)
	for name, data := range map[string][]byte{"jaeger.yaml": jaeger, "jms.yaml": jms} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(context.Background(), store, "jms")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ConnectionProfile != "jaeger" || resolved.Profile.Provider.Type != "opentelemetry" {
		t.Fatalf("unexpected resolved profile: %+v", resolved)
	}
	if resolved.Profile.Provider.Options["index"] != "jaeger-span*" {
		t.Fatalf("opentelemetry options not inherited: %#v", resolved.Profile.Provider.Options)
	}
	if len(resolved.Profile.Aliases) != 2 || resolved.Profile.Aliases[0].Name != "request.xml" || resolved.Profile.Aliases[1].Name != "request.copy" {
		t.Fatalf("alias order not preserved: %+v", resolved.Profile.Aliases)
	}
}

func TestProfileStoreLegacyMigrationRewritesOnce(t *testing.T) {
	store, _ := migratedActivityProfileStore(t)
	path := filepath.Join(store.Dir, "activity.client.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated profile: %v", err)
	}
	var identity struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
	}
	if err := yaml.Unmarshal(data, &identity); err != nil {
		t.Fatalf("parse migrated profile: %v", err)
	}
	if identity.Profile != "activity.client" || identity.Name != "" {
		t.Fatalf("profile was not rewritten to the current identity: %+v", identity)
	}
	if _, err := NewFileStore(store.Dir); err != nil {
		t.Fatalf("second restart should leave migrated profiles unchanged: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile after second restart: %v", err)
	}
	if string(after) != string(data) {
		t.Fatal("second startup rewrote an already migrated profile")
	}
}

func migratedActivityProfileStore(t *testing.T) (*FileStore, []byte) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(context.Background(), sampleProfile("Current Profile")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	legacy := []byte(`name: activity.client
sql:
  root: AsClientActivity
params:
  activity:
    column: AsClientActivity.ActivityGuid
    operator: terms
    required: true
columns:
  - name: ClientGUID
    field: span.ClientGUID
`)
	if err := os.WriteFile(filepath.Join(dir, "activity.client.yaml"), legacy, 0o600); err != nil {
		t.Fatalf("write legacy trace profile: %v", err)
	}
	store, err = NewFileStore(dir)
	if err != nil {
		t.Fatalf("restart profile store: %v", err)
	}
	return store, legacy
}

func TestProfileStoreListRejectsMalformedQueryProfile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	malformed := []byte(`profile: Broken
params:
  region:
    required: true
`)
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), malformed, 0o600); err != nil {
		t.Fatalf("write malformed query profile: %v", err)
	}

	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("expected malformed current query profile to fail listing")
	}
}

func TestProfileStoreSaveRequiresName(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if err := store.Save(context.Background(), query.Profile{}); err == nil {
		t.Fatal("expected error saving a profile with no name")
	}
}

func TestProfileStoreGetMissing(t *testing.T) {
	store, _ := NewFileStore(t.TempDir())
	if _, err := store.Get(context.Background(), "nope"); err == nil {
		t.Fatal("expected error getting a missing profile")
	}
}

func names(ps []query.Profile) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
